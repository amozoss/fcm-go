package main

import (
	"flag"
	"net/http"
	"os"
	"pushie"
	"time"

	"github.com/spacemonkeygo/flagfile"
	"github.com/spacemonkeygo/spacelog"
	"github.com/spacemonkeygo/spacelog/setup"

	"dan/fcm"
)

const (
	defaultConfig = "./flags.conf"
)

var (
	endpoint = "https://fcm.googleapis.com/fcm/send"

	dbPath    = flag.String("db_path", "./store.sqlite3", "path for the database")
	address   = flag.String("address", ":3322", "address for server")
	fcmApiKey = flag.String("fcm_api_key", "", "fcm api key")
	logger    = spacelog.GetLogger()
)

func main() {
	config := flagfile.OptFlagfile(defaultConfig)
	flagfile.Load(config)
	setup.MustSetup(os.Args[0])

	store, err := pushie.NewStore(*dbPath)
	if err != nil {
		logger.Errorf("Error creating store: %v", err)
	}

	fcmClient := fcm.NewFcmClient(endpoint, *fcmApiKey, &http.Client{}, store,
		1*time.Second, 10*time.Second, 5)

	server := pushie.NewServer(fcmClient, store)
	logger.Noticef("Server started listening on %s", *address)
	logger.Error(http.ListenAndServe(*address, server))
}
