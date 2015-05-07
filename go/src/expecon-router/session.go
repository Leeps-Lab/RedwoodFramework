package main

import(
    "time"
    "log"
    "encoding/json"
    "fmt"
    "math"
)

type Session struct {
    db_key            string
    router            *Router
    instance          string
    id                int
    nonce             string
    listeners         map[string]*Listener
    subjects          map[string]*Subject
    msg_cache         []*Msg
    oldest_period    int
    last_state_update map[string]map[string]*Msg
    last_cfg          *Msg
}

func NewSession(r *Router, instance string, id int) (s *Session) {
    s = &Session{
        db_key:            fmt.Sprintf("session:%s:%d", instance, id),
        router:            r,
        instance:          instance,
        id:                id,
        nonce:             uuid(),
        listeners:         make(map[string]*Listener),
        subjects:          make(map[string]*Subject),
        msg_cache:         make([]*Msg, 0),
        oldest_period:    0,
        last_state_update: make(map[string]map[string]*Msg),
        last_cfg:          nil,
    }
    return s
}

func (s *Session) Subject(name string) *Subject {
    subject, exists := s.subjects[name]
    if !exists {
        subject = &Subject{name: name}
        s.subjects[subject.name] = subject
        msg := &Msg{
            Instance: s.instance,
            Session:  s.id,
            Nonce:    s.nonce,
            Sender:   name,
            Time:     time.Now().UnixNano(),
            Key:      "__register__",
            Value:    map[string]string{"user_id": name},
            Period:   0,
            Group:    0,
        }
        s.Receive(msg)
    }
    return subject
}

func (s *Session) Receive(msg *Msg) {
    if msg.Key != "__reset__" && msg.Key != "__delete__" {
        // store message in cache
        s.CacheMessage(msg)
        // store message in database
        if err := s.router.db.SaveMessage(msg); err != nil {
            log.Fatal(err)
        }
    }

    // encode and send message to listener
    bytes, err := json.Marshal(msg)
    if err != nil {
        log.Fatal(err) // not really a good idea to fatal here
    }
    for _, listener := range s.listeners {
        if listener.MatchesMessage(msg) {
            listener.Send(bytes)
        }
    }

    // clean cache if necessary
    should_clean := true
    for _, subject := range s.subjects {

        is_listener := subject.name == "admin" ||
                       subject.name == "listener"

        if subject.period <= s.oldest_period && !is_listener {
            should_clean = false
        }
    }
    s.oldest_period = s.oldestSubjectPeriod()

    if should_clean {
        s.CleanCache()
    }
}

func (s *Session) oldestSubjectPeriod() int {
    oldest_subject_period := math.MaxInt32

    for _, subject := range s.subjects {
        if subject.period < oldest_subject_period {
            oldest_subject_period = subject.period
        }
    }
    if oldest_subject_period == math.MaxInt32 {
        oldest_subject_period = 0
    }

    return oldest_subject_period
}

func (s *Session) CacheMessage(msg *Msg) {
    s.msg_cache = append(s.msg_cache, msg)
}

func (s *Session) CleanCache() {
    log.Printf("Cleaning Cache for session %p...", s)
    log.Printf("(Moved to period %d) %p...", s.oldest_period, s)
    new_cache := make([]*Msg, 0)
    for _, msg := range s.msg_cache {
        if s.MatchesMessage(msg) {
            new_cache = append(new_cache, msg)
        }
    }
    s.msg_cache = new_cache
}

func (s *Session) MatchesMessage(msg *Msg) bool {
    same_period := msg.Period >= s.oldest_period || msg.Period == 0
    last_state_update_msg := s.last_state_update[msg.Key][msg.Sender]
    is_relevant := !msg.StateUpdate || msg.IdenticalTo(last_state_update_msg)

    return msg.IsControlMessage() || (same_period && is_relevant)
}

func (s *Session) Reset() {
    s.nonce = uuid()
    s.subjects = make(map[string]*Subject)
    s.msg_cache = make([]*Msg, 0)
    s.oldest_period = 0
    s.last_state_update = make(map[string]map[string]*Msg)

    sessionID := SessionID{instance: s.instance, id: s.id}
    s.router.db.DeleteSession(sessionID)

    // replay last config
    if s.last_cfg != nil {
        s.last_cfg.Nonce = s.nonce
        s.router.HandleMessage(s.last_cfg)
    }
}

func (s *Session) Delete() {
    s.Reset()
    delete(s.router.sessions[s.instance], s.id)
}