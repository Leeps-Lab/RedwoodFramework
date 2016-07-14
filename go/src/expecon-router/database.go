/*
   database.go

   Manages router data persistence.
*/
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"gopkg.in/redis.v4"
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
		client: redis.NewClient(&redis.Options{
			Addr: redisHost,
			DB:   redisDB,
		}),
	}
	return database
}

/* Getting/Setting Session Stuff */

func (db *Database) SessionIDs() ([]SessionID, error) {
	sessionsData, err := db.client.SMembers("sessions").Result()
	if err != nil {
		return nil, err
	}

	ids := make([]SessionID, len(sessionsData))

	for i, bs := range sessionsData {
		components := strings.Split(string(bs), ":")

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
	sessionObjects, err := db.client.SMembers(key).Result()
	if err != nil {
		return nil, err
	}

	ids := make([]SessionObjectID, len(sessionObjects))

	for i, bs := range sessionObjects {
		components := strings.Split(string(bs), ":")

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
			sessionID: SessionID{
				instance: instance,
				id:       id,
			},
			subject: subject,
		}
	}
	return ids, err
}

func (db *Database) DeleteSession(sessionID SessionID) error {
	_, err := db.client.Del(sessionID.Key()).Result()
	if err != nil {
		return err
	}
	_, err = db.client.SRem("sessions", []byte(sessionID.Key())).Result()
	if err != nil {
		return err
	}
	return db.DeleteSessionObjects(sessionID)
}

/* Getting and Setting Session Objects */

func (db *Database) Period(objectID SessionObjectID) (int, error) {
	i, err := db.client.Get(objectID.Key()).Int64()
	return int(i), err
}

func (db *Database) Group(objectID SessionObjectID) (int, error) {
	i, err := db.client.Get(objectID.Key()).Int64()
	return int(i), err
}

func (db *Database) Config(objectID SessionObjectID) (*Msg, error) {
	bs, err := db.client.Get(objectID.Key()).Bytes()
	if err != nil {
		return nil, err
	}

	var config Msg
	err = json.Unmarshal(bs, &config)
	return &config, err
}

func (db *Database) SetSessionObject(objectID SessionObjectID, data []byte) error {
	keyBytes := []byte(objectID.Key())

	if err := db.client.Set(objectID.Key(), data, 0).Err(); err != nil {
		return err
	}
	return db.client.SAdd(objectID.sessionID.ObjectsKey(), keyBytes).Err()
}

func (db *Database) DeleteSessionObjects(sessionID SessionID) error {
	objectKeys, err := db.client.SMembers(sessionID.Key()).Result()
	if err != nil {
		return err
	}
	for i := range objectKeys {
		if err := db.client.Del(string(objectKeys[i])).Err(); err != nil {
			return err
		}
	}
	return db.client.Del(sessionID.ObjectsKey()).Err()
}

/* Getting Messages */

func (db *Database) Messages(sessionID SessionID) (chan *Msg, error) {
	// retrieve messages in smaller blocks to keep peak memory usage
	// under control when the message digest gets too large
	const blockSize = 1000
	messageCount, err := db.client.LLen(sessionID.Key()).Result()
	if err != nil {
		return nil, err
	}

	messages := make(chan *Msg, blockSize)

	log.Printf("Fetching %d messages from Redis into %p", messageCount, messages)
	go func() {
		defer close(messages)
		for i := int64(0); i < messageCount; i += blockSize {
			limit := i + blockSize
			if limit >= messageCount {
				limit = messageCount
			}
			msgData, err := db.client.LRange(sessionID.Key(), i, limit-1).Result()
			if err != nil {
				return
			}
			for _, bs := range msgData {
				var msg Msg
				if err = json.Unmarshal([]byte(bs), &msg); err != nil {
					return
				}
				messages <- &msg
			}
		}
	}()

	return messages, nil
}

/* Saving Messages */

func (db *Database) SaveMessage(msg *Msg) error {
	key := fmt.Sprintf("session:%s:%d", msg.Instance, msg.Session)
	db.client.SAdd("sessions", []byte(key))
	bs, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return db.client.RPush(key, bs).Err()
}
