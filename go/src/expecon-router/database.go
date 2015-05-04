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
    "log"
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

type PeriodID struct {
    session SessionID
    period  int
}

func (p PeriodID) Key() string {
    return fmt.Sprintf("session:%s:%d:%d", p.session.instance, p.session.id, p.period)
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
    client        *redis.Client
}

func NewDatabase(redisHost string, redisDB int) (database *Database) {
    database = &Database{
        client: &redis.Client{Addr: redisHost, Db: redisDB},
    }
    return database
}

/* Getting/Setting Session Stuff */

func (db *Database) SessionIDs() ([]SessionID, error) {
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

func (db *Database) SessionObjectIDs(sessionID SessionID) ([]SessionObjectID, error) {
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

func (db *Database) DeleteSession(sessionID SessionID) (error) {
    // delete session messages
    _, err := db.client.Del(sessionID.Key())
    if err != nil {
        return err
    }

    // delete session messages
    var keys []string;
    keys, err = db.client.Keys(sessionID.Key() + ":*")
    for _, key := range keys {
        _, err := db.client.Del(key)
        if err != nil {
            return err
        }   
    }

    // delete session from sessions set
    _, err = db.client.Srem("sessions", []byte(sessionID.Key()))
    if err != nil {
        return err
    }

    // delete session objects
    return db.DeleteSessionObjects(sessionID)
}

/* Getting and Setting Session Objects */

func (db *Database) getIntData(key string) (int, error) {
    bytes, err := db.client.Get(key)
    if err != nil {
        return 0, err
    }
    return strconv.Atoi(string(bytes))
}

func (db *Database) Period(objectID SessionObjectID) (int, error) {
    return db.getIntData(objectID.Key())
}

func (db *Database) Group(objectID SessionObjectID) (int, error) {
    return db.getIntData(objectID.Key())
}

func (db *Database) Config(objectID SessionObjectID) (*Msg, error) {
    bytes, err := db.client.Get(objectID.Key())
    if err != nil {
        return nil, err
    }

    var config Msg
    err = json.Unmarshal(bytes, &config)
    return &config, err
}

func (db *Database) SetSessionObject(objectID SessionObjectID, data []byte) (error) {
    keyBytes := []byte(objectID.Key())

    var err error
    if err = db.client.Set(objectID.Key(), data); err != nil {
        return err
    }
    if _, err = db.client.Sadd(objectID.sessionID.ObjectsKey(), keyBytes); err != nil {
        return err
    }
    return nil
}

func (db *Database) DeleteSessionObjects(sessionID SessionID) (error) {
    objectKeys, err := db.client.Smembers(sessionID.Key())
    if err != nil {
        return err
    }
    for i := range objectKeys {
        _, err := db.client.Del(string(objectKeys[i]))
        if err != nil {
            return err
        }
    }
    _, err = db.client.Del(sessionID.ObjectsKey())
    return err
}

/* Getting Messages */

func (db *Database) Messages(sessionID SessionID) (chan *Msg, error) {
    // retrieve messages in smaller blocks to keep peak memory usage
    // under control when the message digest gets too large
    blockSize := 1000
    messageCount, err := db.client.Llen(sessionID.Key())
    if err != nil {
        return nil, err
    }

    messages := make(chan *Msg, blockSize)

    log.Printf("Fetching %d messages from Redis into %p", messageCount, messages)
    go func() {
        defer close(messages)
        for i := 0; i < messageCount; i += blockSize {
            limit := i + blockSize
            if limit >= messageCount {
                limit = messageCount
            }
            msgData, err := db.client.Lrange(sessionID.Key(), i, limit - 1)
            if err != nil {
                return
            }
            for _, bytes := range msgData {
                var msg Msg
                if err = json.Unmarshal(bytes, &msg); err != nil {
                    return
                }
                messages <- &msg
            }
        }
    }()

    return messages, nil
}

// gets messages for this period and all get/set messages
func (db *Database) PeriodMessages(periodID PeriodID) (chan *Msg, error) {
    // retrieve messages in smaller blocks to keep peak memory usage
    // under control when the message digest gets too large
    blockSize := 1000
    periodKey := periodID.Key()
    messageCount, err := db.client.Llen(periodKey)
    if err != nil {
        return nil, err
    }

    messages := make(chan *Msg, blockSize)

    log.Printf("Fetching %d messages from Redis into %p", messageCount, messages)
    go func() {
        defer close(messages)
        for i := 0; i < messageCount; i += blockSize {
            limit := i + blockSize
            if limit >= messageCount {
                limit = messageCount
            }
            msgData, err := db.client.Lrange(periodKey, i, limit - 1)
            if err != nil {
                return
            }
            for _, bytes := range msgData {
                var msg Msg
                if err = json.Unmarshal(bytes, &msg); err != nil {
                    return
                }
                messages <- &msg
            }
        }
    }()

    return messages, nil
}

/* Saving Messages */

func (db *Database) SaveMessage(msg *Msg) (error) {
    sessionID := SessionID{msg.Instance, msg.Session}
    periodID := PeriodID{sessionID, msg.Period}
    key := sessionID.Key();

    db.client.Sadd("sessions", []byte(key))
    if b, err := json.Marshal(msg); err == nil {
        err := db.client.Rpush(key, b)
        err = db.client.Rpush(periodID.Key(), b)
        return err
    }
    return nil
}