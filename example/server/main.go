package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/amozoss/fcm-go"
	"github.com/spacemonkeygo/flagfile"
	"github.com/spacemonkeygo/spacelog"
	"github.com/spacemonkeygo/spacelog/setup"
)

const (
	defaultConfig = "./flags.conf"
)

var (
	address   = flag.String("address", ":8080", "address for server")
	fcmApiKey = flag.String("fcm_api_key", "", "fcm api key")
	logger    = spacelog.GetLogger()
)

func main() {
	config := flagfile.OptFlagfile(defaultConfig)
	flagfile.Load(config)
	setup.MustSetup(os.Args[0])

	store := NewMemStore()

	fcmClient := fcm.NewDefaultClient(*fcmApiKey, store)
	server := NewServer(fcmClient, store)
	logger.Noticef("Server started listening on %s", *address)
	logger.Error(http.ListenAndServe(*address, server))
}

type Server struct {
	fcmClient fcm.FcmClient
	store     *MemStore
	http.Handler
}

func NewServer(fcmClient fcm.FcmClient, store *MemStore) *Server {
	s := &Server{
		fcmClient: fcmClient,
		store:     store,
	}
	mux := http.NewServeMux()
	mux.Handle("/simple", http.HandlerFunc(s.simple))
	mux.Handle("/add", http.HandlerFunc(s.add))
	mux.Handle("/message", http.HandlerFunc(s.message))
	s.Handler = mux
	return s
}

// Send title and body as form data
// curl localhost:8080/simple -d "title=Hello" -d "body=world"
func (s *Server) simple(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	title := r.PostFormValue("title")
	body := r.PostFormValue("body")
	ctx := context.TODO()
	if _, err := s.fcmClient.Send(ctx, fcm.HttpMessage{
		RegistrationIds: s.store.List(),
		Notification: &fcm.Notification{
			Title: title,
			Body:  body,
		},
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(200)
}

// curl localhost:8080/add -d "regId=12ab34"
func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	regId := r.PostFormValue("regId")
	s.store.Add(regId)
	w.WriteHeader(201)
}

// Send fcm.HttpMessage as json
func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	var msg fcm.HttpMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := context.TODO()
	if _, err := s.fcmClient.Send(ctx, msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(200)
}

type MemStore struct {
	regIds map[string]bool
}

func NewMemStore() *MemStore {
	return &MemStore{
		regIds: make(map[string]bool),
	}
}

func (m *MemStore) Update(ctx context.Context, oldRegId, newRegId string) error {
	if _, ok := m.regIds[oldRegId]; ok {
		delete(m.regIds, oldRegId)
		m.regIds[newRegId] = true
		return nil
	}
	return fmt.Errorf("Update error: Could not find RegID: %s ", oldRegId)
}

func (m *MemStore) Delete(ctx context.Context, regId string) error {
	if _, ok := m.regIds[regId]; ok {
		delete(m.regIds, regId)
	}

	return fmt.Errorf("Delete error: Could not find RegID: %s ", regId)
}

func (m *MemStore) Add(regId string) {
	m.regIds[regId] = true
}

func (m *MemStore) List() (regIds []string) {
	for regId := range m.regIds {
		regIds = append(regIds, regId)
	}
	return regIds
}
