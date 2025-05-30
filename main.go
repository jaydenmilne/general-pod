package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jaydenmilne/podcast/podcast"
	"github.com/jaydenmilne/podcast/rss"
	_ "github.com/mattn/go-sqlite3"
	"github.com/tcolgate/mp3"
)

const BaseAddr = "https://github.com/jaydenmilne/general-pod"

var f = false

func Duration(mp3bytes []byte) time.Duration {
	decoder := mp3.NewDecoder(bytes.NewReader(mp3bytes))
	totalDuration := 0.0

	var frame mp3.Frame
	var skipped int
	for {
		if err := decoder.Decode(&frame, &skipped); err != nil {
			if err == io.EOF {
				break
			}
			log.Fatal(err)
		}
		totalDuration += frame.Duration().Seconds()
	}

	return time.Second * time.Duration(totalDuration)

}

func saveUpdatedEpisode(db *sql.Tx, id int64, guid string, length int, lengthSeconds float64) {
	_, err := db.Exec("UPDATE episodes SET guid = COALESCE(guid, ?), length_bytes = ?, length_seconds = ? WHERE episodes.episode_id = ?", guid, length, lengthSeconds, id)

	if err != nil {
		log.Fatal(err)
	}

}

func updateEpisodes(db *sql.DB) {

	tx, _ := db.BeginTx(context.Background(), nil)

	result, err := tx.Query("SELECT episode_id, audio_url FROM episodes WHERE guid IS NULL OR LENGTH_BYTES IS NULL OR LENGTH_SECONDS IS NULL")

	if err != nil {
	}

	defer result.Close()
	for result.Next() {
		var episodeId int64
		var audioUrl string

		if err := result.Scan(&episodeId, &audioUrl); err != nil {
			log.Fatal(err)
		}

		fmt.Printf("got %d %s\n", episodeId, audioUrl)

		result, err := http.Get(audioUrl)

		if err != nil {
			log.Fatal(err)
		}

		mp3Bytes, err := io.ReadAll(result.Body)

		if err != nil {
			log.Fatal(err)
		}

		fmt.Printf("mp3 bytes: %d\n", len(mp3Bytes))

		timeLength := Duration(mp3Bytes)

		fmt.Printf("length: %f\n", timeLength.Seconds())

		id := uuid.New()

		saveUpdatedEpisode(tx, episodeId, id.String(), len(mp3Bytes), timeLength.Seconds())
	}

	tx.Commit()

}

var sessionLookup = map[string]string{
	"sat_morn_complete": "Saturday Morning (complete session)",
	"sat_morn":          "Saturday Morning",
	"sat_even":          "Saturday Evening",
	"sun_morn":          "Sunday Morning",
	"sun_even":          "Sunday Evening",
}

func getPersonURL(db *sql.DB, name string) (url, image string) {
	result := db.QueryRow("SELECT url, image_url FROM people WHERE name = ?", name)
	if result.Err() != nil {
		return "", ""
	}

	if result.Scan(&url, &image) != nil {
		return "", ""
	}

	return
}

func addEpisodes(db *sql.DB, pod *podcast.RSSPodcast) {
	result, err := db.Query(`
		SELECT
			episode_id
			,conference_month
			,conference_year
			,session
			,talk_name
			,talk_url
			,guid
			,pub_date
			,season
			,episode
			,speaker
			,speaker_title
			,audio_url
			,transcript_url
			,length_bytes
			,length_seconds
		FROM
			episodes
		ORDER BY episode_id DESC;
	`)

	if err != nil {
		log.Fatal(err)
	}

	defer result.Close()
	for result.Next() {
		var session, conference_month, talk_name, talk_url, guid, speaker, speaker_title, audio_url, transcript_url sql.NullString
		var conference_year, episode_id, pub_date, season, episode, length_bytes int
		var length_seconds float64

		if err := result.Scan(
			&episode_id,
			&conference_month,
			&conference_year,
			&session,
			&talk_name,
			&talk_url,
			&guid,
			&pub_date,
			&season,
			&episode,
			&speaker,
			&speaker_title,
			&audio_url,
			&transcript_url,
			&length_bytes,
			&length_seconds,
		); err != nil {
			log.Fatal(err)
		}

		personURL, personImageURL := getPersonURL(db, speaker.String)
		podEpisode := podcast.Episode{
			Item: rss.Item{
				Title: fmt.Sprintf("%s by %s", talk_name.String, speaker.String),
				Link:  talk_url.String,
				Description: &rss.Description{
					Value: fmt.Sprintf("%s by %s, %s\nLink: %s", talk_name.String, speaker.String, speaker_title.String, talk_url.String),
				},
				Enclosure: &rss.Enclosure{
					URL:    audio_url.String,
					Length: length_bytes,
					Type:   "audio/mpeg",
				},
				GUID: &rss.GUID{
					IsPermaLink: &f,
					Value:       guid.String,
				},
				PubDate: rss.RFC2822Date(time.Unix(int64(pub_date), 0).Format(rss.RFC2822DateFormatSpecifier)),
			},

			ItunesDuration: fmt.Sprintf("%f", length_seconds),
			ItunesExplicit: &f,
			ItunesEpisode:  episode_id,
			ItunesSeason:   season,
			PodcastTranscript: []podcast.PodcastTranscript{{
				URL:  transcript_url.String,
				Type: "text/vtt",
			}},
			PodcastPeople: []podcast.PodcastPerson{
				{
					PersonName: speaker.String,
					Role:       "Author",
					Href:       personURL,
					Img:        personImageURL,
				},
			},

			PodcastSeason: &podcast.PodcastSeason{
				SeasonNumber: season,
				Name:         fmt.Sprintf("%s %d Conference (%d)", conference_month.String, conference_year, season),
			},

			PodcastEpisode: &podcast.PodcastEpisode{
				EpisodeNumber: float32(episode),
			},
		}

		if strings.HasSuffix(session.String, "complete") {
			podEpisode.Title = fmt.Sprintf("%s (complete)", talk_name.String)
			podEpisode.Description.Value = fmt.Sprintf("%s (complete)\nLink: %s", talk_name.String, talk_url.String)
		}

		pod.Channel.Items = append(pod.Channel.Items, podEpisode)

	}
}

func main() {
	podcast := podcast.RSSPodcast{
		Version: rss.RSSVersion,
		Channel: podcast.Podcast{
			Channel: rss.Channel{
				Title: "General Conference (Unofficial)",
				Link:  "https://generalpod.jayd.ml",
				Description: rss.Description{
					Value: `The unofficial podcast feed for talks from the General Conference of the Church of Jesus Christ of Latter Day Saints. This podcast is unaffiliated with the Church of Jesus Christ of Latter Day Saints.`,
				},
				Language:  "en-us",
				Generator: BaseAddr,
				Docs:      "https://www.rssboard.org/rss-specification",
				// TODO: Image: ,
			},
			ItunesImage: podcast.ItunesImageTag{
				Href: BaseAddr + "/docs/assets/en-us/itunes_image.jpg",
			},

			ItunesCategory: []podcast.ItunesCategory{
				{
					Text: "Religion & Spirituality",
					SubCategory: &struct {
						XMLName xml.Name "xml:\"http://www.itunes.com/dtds/podcast-1.0.dtd category\""
						Text    string   "xml:\"text,attr\""
					}{
						Text: "Christianity",
					},
				},
			},
			ItunesAuthor:   "Jayden Milne",
			ItunesExplicit: &f,
			ItunesType:     podcast.ItunesShowTypeEpisodic,
			PodcastLocation: &podcast.PodcastLocation{
				LocationName: "Conference Center",
				Geo:          "geo:40.7725,-111.8925",
				Osm:          "R6146196",
			},
			Items: make([]podcast.Episode, 0),
		},
	}

	db, err := sql.Open("sqlite3", "./podcast.db?journal_mode=WAL&mode=rwc")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	updateEpisodes(db)
	addEpisodes(db, &podcast)
	podcast.Channel.PubDate = rss.RFC2822Date(time.Now().Format(rss.RFC2822DateFormatSpecifier))
	podcast.Channel.LastBuildDate = rss.RFC2822Date(time.Now().Format(rss.RFC2822DateFormatSpecifier))

	xmlBytes, _ := xml.Marshal(&podcast)

	os.WriteFile("docs/podcast/en-us/feed.xml", xmlBytes, 0644)

}
