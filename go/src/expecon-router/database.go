/*
    database.go

    Manages router data persistence.
*/
package main

import(
    "redis-go"
    "encoding/json"
    "strconv"
    "strings"
    "errors"
    "fmt"
)

type SessionID struct {
    instance string
    id       int
}

func (s SessionID) Key() string {
    return fmt.Sprintf("session:%s:%d", s.instance, s.id)
}

func (s SessionID) ObjectsKey() string {
    return fmt.Sprintf("session_objs:%s:%d", s.instance, s.id)
}

type SessionObjectID struct {
    objectType string
    sessionID  SessionID
    subject    string
}

func (s SessionObjectID) Key() string {
    return fmt.Sprintf("%s:%s:%d:%s", s.objectType, s.sessionID.instance, s.sessionID.id, s.subject)
}

type Database struct {
    client *redis.Client
}

func NewDatabase(redisHost string, redisDB int) (database *Database) {
    database = &Database{
        client: &redis.Client{Addr: redisHost, Db: redisDB},
    }
    return database
}

func (db *Database) GetSessionIDs() ([]SessionID, error) {
    sessionsData, err := db.client.Smembers("sessions")
    if err != nil {
        return nil, err
    }

    ids := make([]SessionID, len(sessionsData))

    for i, bytes := range sessionsData {
        components := strings.Split(string(bytes), ":")

        instance := components[1]
        id, err := strconv.Atoi(components[2])
        if err != nil {
            return ids, err
        }

        ids[i] = SessionID{
            instance: instance,
            id:       id,
        }
    }
    return ids, err
}

func (db *Database) GetSessionObjectIDs(sessionID SessionID) ([]SessionObjectID, error) {
    key := sessionID.ObjectsKey()
    sessionObjects, err := db.client.Smembers(key)
    if err != nil {
        return nil, err
    }

    ids := make([]SessionObjectID, len(sessionObjects))

    for i, bytes := range sessionObjects {
        components := strings.Split(string(bytes), ":")

        objectType := components[0]
        instance := components[1]
        id, err := strconv.Atoi(components[2])
        if err != nil {
            return ids, err
        }
        if instance != sessionID.instance || id != sessionID.id {
            return ids, errors.New("session_objs has object with different instance/id")
        }
        subject := components[3]

        ids[i] = SessionObjectID{
            objectType: objectType,
            sessionID:  SessionID{
                            instance: instance,
                            id:       id,
                        },
            subject:    subject,
        }
    }
    return ids, err
}

/* Getting Session Objects */

func (db *Database) getIntData(key string) (int, error) {
    bytes, err := db.client.Get(key)
    if err != nil {
        return 0, err
    }
    return strconv.Atoi(string(bytes))
}

func (db *Database) GetPeriod(objectID SessionObjectID) (int, error) {
    return db.getIntData(objectID.Key())
}

func (db *Database) GetGroup(objectID SessionObjectID) (int, error) {
    return db.getIntData(objectID.Key())
}

func (db *Database) GetConfig(objectID SessionObjectID) (*Msg, error) {
    bytes, err := db.client.Get(objectID.Key())
    if err != nil {
        return nil, err
    }

    var config Msg
    err = json.Unmarshal(bytes, &config)
    return &config, err
}

/* Gettings Messages */

func (db *Database) GetMessages(sessionID SessionID) (chan *Msg, error) {
    // retrive messages in smaller blocks to keep peak memory usage
    // under control when the message digest gets too large
    blockSize := 1000
    messageCount, err := db.client.Llen(sessionID.Key())
    if err != nil {
        return nil, err
    }

    messages := make(chan *Msg, blockSize)

    go func() {
        for i := 0; i <= messageCount; i += blockSize {
            limit := i + blockSize
            if limit > messageCount {
                limit = messageCount
            }
            msgData, err := db.client.Lrange(sessionID.Key(), i, limit)
            if err != nil {
                close(messages)
                return
            }
            for _, bytes := range msgData {
                var msg Msg
                if err = json.Unmarshal(bytes, &msg); err != nil {
                    close(messages)
                    return
                }
                messages <- &msg
            }
        }
        close(messages)
    }()

    return messages, nil
}
