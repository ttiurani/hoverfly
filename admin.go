package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	// static assets
	_ "github.com/SpectoLabs/hoverfly/statik"
	"github.com/rakyll/statik/fs"

	log "github.com/Sirupsen/logrus"
	"github.com/codegangsta/negroni"
	"github.com/go-zoo/bone"
	"github.com/gorilla/websocket"
	"github.com/meatballhat/negroni-logrus"
)

// recordedRequests struct encapsulates payload data
type recordedRequests struct {
	Data []Payload `json:"data"`
}

type recordsCount struct {
	Count int `json:"count"`
}

type statsResponse struct {
	Stats HoverflyStats `json:"stats"`
}

type stateRequest struct {
	Mode        string `json:"mode"`
	Destination string `json:"destination"`
}

type messageResponse struct {
	Message string `json:"message"`
}

func (d *DBClient) startAdminInterface() {
	// starting admin interface
	mux := getBoneRouter(*d)
	n := negroni.Classic()

	loglevel := log.WarnLevel

	if d.cfg.verbose {
		loglevel = log.DebugLevel
	}

	n.Use(negronilogrus.NewCustomMiddleware(loglevel, &log.JSONFormatter{}, "admin"))
	n.UseHandler(mux)

	// admin interface starting message
	log.WithFields(log.Fields{
		"AdminPort": d.cfg.adminPort,
	}).Info("Admin interface is starting...")

	n.Run(fmt.Sprintf(":%s", d.cfg.adminPort))
}

// getBoneRouter returns mux for admin interface
func getBoneRouter(d DBClient) *bone.Mux {
	mux := bone.New()

	// preparing static assets for embedded admin
	statikFS, err := fs.New()
	if err != nil {
		log.WithFields(log.Fields{
			"Error": err.Error(),
		}).Error("Failed to load statikFS, admin UI might not work :(")
	}

	mux.Get("/records", http.HandlerFunc(d.AllRecordsHandler))
	mux.Delete("/records", http.HandlerFunc(d.DeleteAllRecordsHandler))
	mux.Post("/records", http.HandlerFunc(d.ImportRecordsHandler))

	mux.Get("/count", http.HandlerFunc(d.RecordsCount))
	mux.Get("/stats", http.HandlerFunc(d.StatsHandler))
	mux.Get("/statsws", http.HandlerFunc(d.StatsWSHandler))

	mux.Get("/state", http.HandlerFunc(d.CurrentStateHandler))
	mux.Post("/state", http.HandlerFunc(d.StateHandler))

	mux.Handle("/*", http.FileServer(statikFS))

	return mux
}

// AllRecordsHandler returns JSON content type http response
func (d *DBClient) AllRecordsHandler(w http.ResponseWriter, req *http.Request) {
	records, err := d.cache.GetAllRequests()

	if err == nil {

		w.Header().Set("Content-Type", "application/json")

		var response recordedRequests
		response.Data = records
		b, err := json.Marshal(response)

		if err != nil {
			log.Error(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			w.Write(b)
			return
		}
	} else {
		log.WithFields(log.Fields{
			"Error": err.Error(),
		}).Error("Failed to get data from cache!")

		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(500) // can't process this entity
		return
	}
}

// RecordsCount returns number of captured requests as a JSON payload
func (d *DBClient) RecordsCount(w http.ResponseWriter, req *http.Request) {
	records, err := d.cache.GetAllRequests()

	if err == nil {

		w.Header().Set("Content-Type", "application/json")

		var response recordsCount
		response.Count = len(records)
		b, err := json.Marshal(response)

		if err != nil {
			log.Error(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			w.Write(b)
			return
		}
	} else {
		log.WithFields(log.Fields{
			"Error": err.Error(),
		}).Error("Failed to get data from cache!")

		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(500) // can't process this entity
		return
	}
}

func (d *DBClient) StatsHandler(w http.ResponseWriter, req *http.Request) {
	stats := d.counter.Flush()

	var sr statsResponse
	sr.Stats = stats

	w.Header().Set("Content-Type", "application/json")

	b, err := json.Marshal(sr)

	if err != nil {
		log.Error(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		w.Write(b)
		return
	}

}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// categoryWSFilterHandler is used for searching categories based on names and keywords through the websocket
func (d *DBClient) StatsWSHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	for {
		messageType, p, err := conn.ReadMessage()
		if err != nil {
			return
		}
		log.WithFields(log.Fields{
			"message": string(p),
		}).Info("Got message...")

		for _ = range time.Tick(1 * time.Second) {

			stats := d.counter.Flush()
			var sr statsResponse
			sr.Stats = stats

			b, err := json.Marshal(sr)

			if err = conn.WriteMessage(messageType, b); err != nil {
				log.WithFields(log.Fields{
					"message": p,
					"error":   err.Error(),
				}).Error("Got error when writing message...")
				return
			}
		}

	}

}

// ImportRecordsHandler - accepts JSON payload and saves it to cache
func (d *DBClient) ImportRecordsHandler(w http.ResponseWriter, req *http.Request) {

	var requests recordedRequests

	defer req.Body.Close()
	body, err := ioutil.ReadAll(req.Body)

	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	var response messageResponse

	if err != nil {
		// failed to read response body
		log.WithFields(log.Fields{
			"error": err.Error(),
		}).Error("Could not read response body!")
		response.Message = "Bad request. Nothing to import!"
		http.Error(w, "Failed to read request body.", 400)
		return
	}

	err = json.Unmarshal(body, &requests)

	if err != nil {
		w.WriteHeader(422) // can't process this entity
		return
	}

	payloads := requests.Data
	if len(payloads) > 0 {
		for _, pl := range payloads {
			bts, err := pl.encode()
			if err != nil {
				log.WithFields(log.Fields{
					"error": err.Error(),
				}).Error("Failed to encode payload")
			} else {
				// recalculating request hash and storing it in database
				r := request{details: pl.Request}
				d.cache.Set([]byte(r.hash()), bts)
			}
		}
		response.Message = fmt.Sprintf("%d requests imported successfully", len(payloads))
	} else {
		response.Message = "Bad request. Nothing to import!"
		w.WriteHeader(400)
	}

	b, err := json.Marshal(response)
	w.Write(b)

}

// DeleteAllRecordsHandler - deletes all captured requests
func (d *DBClient) DeleteAllRecordsHandler(w http.ResponseWriter, req *http.Request) {
	err := d.cache.DeleteBucket(d.cache.requestsBucket)

	w.Header().Set("Content-Type", "application/json")

	var response messageResponse
	if err != nil {
		if err.Error() == "bucket not found" {
			response.Message = fmt.Sprintf("No records found")
			w.WriteHeader(200)
		} else {
			response.Message = fmt.Sprintf("Something went wrong: %s", err.Error())
			w.WriteHeader(500)
		}
	} else {
		response.Message = "Proxy cache deleted successfuly"
		w.WriteHeader(200)
	}
	b, err := json.Marshal(response)

	w.Write(b)
	return
}

// CurrentStateHandler returns current state
func (d *DBClient) CurrentStateHandler(w http.ResponseWriter, req *http.Request) {
	var resp stateRequest
	resp.Mode = d.cfg.GetMode()
	resp.Destination = d.cfg.destination

	b, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Write(b)
}

// StateHandler handles current proxy state
func (d *DBClient) StateHandler(w http.ResponseWriter, r *http.Request) {
	var sr stateRequest

	// this is mainly for testing, since when you create
	if r.Body == nil {
		r.Body = ioutil.NopCloser(bytes.NewBuffer([]byte("")))
	}

	defer r.Body.Close()
	body, err := ioutil.ReadAll(r.Body)

	if err != nil {
		// failed to read response body
		log.WithFields(log.Fields{
			"error": err.Error(),
		}).Error("Could not read response body!")
		http.Error(w, "Failed to read request body.", 400)
		return
	}

	err = json.Unmarshal(body, &sr)

	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(400) // can't process this entity
		return
	}

	availableModes := map[string]bool{
		"virtualize": true,
		"capture":    true,
		"modify":     true,
		"synthesize": true,
	}

	if !availableModes[sr.Mode] {
		log.WithFields(log.Fields{
			"suppliedMode": sr.Mode,
		}).Error("Wrong mode found, can't change state")
		http.Error(w, "Bad mode supplied, available modes: virtualize, capture, modify, synthesize.", 400)
		return
	}

	log.WithFields(log.Fields{
		"newState": sr.Mode,
		"body":     string(body),
	}).Info("Handling state change request!")

	// setting new state
	d.cfg.SetMode(sr.Mode)

	var resp stateRequest
	resp.Mode = d.cfg.GetMode()
	resp.Destination = d.cfg.destination
	b, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Write(b)

}
