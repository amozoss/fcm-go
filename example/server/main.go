package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/spacemonkeygo/flagfile"
	"github.com/spacemonkeygo/spacelog"
	"github.com/spacemonkeygo/spacelog/setup"

	"dan/fcm"
)

const (
	defaultConfig = "./flags.conf"
)

var (
	address   = flag.String("address", ":3322", "address for server")
	fcmApiKey = flag.String("fcm_api_key", "", "fcm api key")
	logger    = spacelog.GetLogger()
)

func main() {
	config := flagfile.OptFlagfile(defaultConfig)
	flagfile.Load(config)
	setup.MustSetup(os.Args[0])

	fcmClient := fcm.NewFcmClient(*fcmApiKey, http.DefaultClient, store, nil)
	server := pushie.NewServer(fcmClient, store)
	logger.Noticef("Server started listening on %s", *address)
	logger.Error(http.ListenAndServe(*address, server))
}

type MemStore struct {
	regIds map[string]bool
}

func NewMemStore() *MemStore {
	return &MemStore{
		regIds: make(map[string]bool),
	}
}

func (m *MemStore) Update(oldRegId, newRegId string) error {
	if _, ok := m.regIds[oldRegId]; ok {
		m.regIds[oldRegId] = newRegId
		return nil
	}
	return fmt.Errorf("Update error: Could not find RegID: %s ", oldRegId)
}

func (m *MemStore) Delete(regId string) error {
	if _, ok := m.regIds[oldRegId]; ok {
		delete(m.regIds, oldRegId)
	}

	return fmt.Errorf("Delete error: Could not find RegID: %s ", regId)
}
