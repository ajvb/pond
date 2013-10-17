package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	_ "github.com/mattn/go-sqlite3"
	"github.com/moovweb/gokogiri"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
)

type Subscription struct {
	Id     int    `json:"id"`
	UserId int    `json:"-"`
	Url    string `json:"url"`
	Name   string `json:"name"`
}

func removeTrail(rawurl string) string {
	if rawurl[len(rawurl)-1] == '/' {
		return rawurl[:len(rawurl)-1]
	}
	return rawurl
}

func findRSSURL(rawurl string) (string, error) {
	res, err := http.Get(rawurl)
	if err != nil {
		return "", err
	}
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	doc, err := gokogiri.ParseXml(body)
	defer doc.Free()
	if (doc.Root().Name() != "rss" && doc.Root().Name() != "feed") || err != nil {
		doc, _ := gokogiri.ParseHtml(body)
		defer doc.Free()
		doc.RecursivelyRemoveNamespaces()
		nodes, err := doc.Search("//link")
		if err != nil {
			return "", err
		}
		for _, v := range nodes {
			if v.Attribute("rel").Value() == "alternate" || v.Attribute("rel").Value() == "feed" {
				u, _ := url.Parse(v.Attribute("href").Value())
				if u.IsAbs() {
					return u.String(), nil
				} else {
					u.Host = removeTrail(rawurl)
					return u.String(), nil
				}
			}
		}
	} else {
		return res.Request.URL.String(), nil
	}
	return "", errors.New("Feed URL not found")
}

func findRSSTitle(rssUrl string) (string, error) {
	res, err := http.Get(rssUrl)
	if err != nil {
		return "", err
	}
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	doc, err := gokogiri.ParseXml(body)
	defer doc.Free()
	doc.RecursivelyRemoveNamespaces()
	nodes, err := doc.Search("//title")
	if err != nil {
		return "", err
	}
	if len(nodes) > 0 {
		return nodes[0].Content(), nil
	}
	return "", nil
}

func PostSubscription(w http.ResponseWriter, req *http.Request) {
	sessionToken := req.Header.Get("x-session-token")
	if sessionToken == "" {
		WriteJSONError(w, http.StatusBadRequest, "Session token not provided")
		return
	}
	u, err, code := GetUserForSessionToken(sessionToken)
	if err != nil {
		WriteJSONError(w, code, err.Error())
	} else {
		if rawurl := req.FormValue("url"); rawurl != "" {
			rssUrl, err := findRSSURL(rawurl)
			if err != nil {
				// TODO: Write error func. How should the program decide what status code to send?
				w.Header().Set("content-type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error": "` + err.Error() + `"}`))
				log.Printf("POST Subscription error (Find URL error): %s", err.Error())
			} else {
				title, err := findRSSTitle(rssUrl)
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(""))
					log.Printf("POST Subscription error (Find title error): %s", err.Error())
				}
				DB, _ := sql.Open("sqlite3", ExePath+"/db.db")
				defer DB.Close()
				var sub Subscription
				rowErr := DB.QueryRow("select * from subscriptions where url=?", rssUrl).Scan(&sub.Id, &sub.Url, &sub.Name)
				if rowErr == sql.ErrNoRows {
					DB.Exec("insert into subscriptions values (null, ?, ?, ?)", u.Id, rssUrl, title)
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte(""))
				} else {
					w.Header().Set("content-type", "application/json")
					w.WriteHeader(http.StatusConflict)
					w.Write([]byte(`{"error": "Feed already exists"}`))
				}
			}
		} else {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"Insufficient parameters: URL was not provided"}`))
		}
	}
}

func GetSubscriptions(w http.ResponseWriter, req *http.Request) {
	sessionToken := req.Header.Get("x-session-token")
	if sessionToken == "" {
		WriteJSONError(w, http.StatusBadRequest, "Session token not provided")
		return
	}
	u, err, code := GetUserForSessionToken(sessionToken)
	if err != nil {
		WriteJSONError(w, code, err.Error())
	} else {
		DB, _ := sql.Open("sqlite3", ExePath+"/db.db")
		rows, err := DB.Query("select id, url, name from subscriptions where user_id = ?", u.Id)
		DB.Close()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(""))
			log.Printf("GET Subscriptions error (Database error): %s", err.Error())
		}
		subs := make([]Subscription, 0)
		for rows.Next() {
			var sub Subscription
			rows.Scan(&sub.Id, &sub.Url, &sub.Name)
			subs = append(subs, sub)
		}
		rows.Close()
		enc := json.NewEncoder(w)
		w.Header().Set("content-type", "application/json")
		encErr := enc.Encode(subs)
		if encErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(""))
			log.Printf("GET Subscriptions error (JSON encoding error): %s", err.Error())
		}
	}
}

func DeleteSubscription(w http.ResponseWriter, req *http.Request) {
	sessionToken := req.Header.Get("x-session-token")
	if sessionToken == "" {
		WriteJSONError(w, http.StatusBadRequest, "Session token not provided")
		return
	}
	u, err, code := GetUserForSessionToken(sessionToken)
	if err != nil {
		WriteJSONError(w, code, err.Error())
	} else {
		id := req.URL.Query().Get(":id")
		var sub Subscription
		DB, _ := sql.Open("sqlite3", ExePath+"/db.db")
		defer DB.Close()
		err := DB.QueryRow("select * from subscriptions where id = ? and user_id = ?", id, u.Id).Scan(&sub.Id, &sub.UserId, &sub.Url, &sub.Name)
		switch {
		case err == sql.ErrNoRows:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("Subscription does not exist"))
		case err != nil:
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(""))
			log.Printf("DELETE subscription error (Database error): %s", err.Error())
		default:
			DB.Exec("delete from subscriptions where id=?", id)
		}
	}
}
