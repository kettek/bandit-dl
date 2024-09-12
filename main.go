package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/bogem/id3v2/v2"
	"golang.org/x/net/html"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: bandit-dl <album-url> [<album-url> ...]")
		return
	}

	for _, url := range os.Args[1:] {
		if err := downloadAlbum(url); err != nil {
			fmt.Println("❌", err)
		}
	}

	fmt.Println("🎶 Thanks for using this tool and remember to support the musicians!")
}

type timestamp struct {
	time.Time
}

func (t *timestamp) UnmarshalJSON(b []byte) error {
	s := string(b)
	s = s[1 : len(s)-1]
	tt, err := time.Parse("02 Jan 2006 15:04:05 GMT", s)
	if err != nil {
		return err
	}
	t.Time = tt
	return nil
}

type bandcampTRAlbum struct {
	Artist  string `json:"artist"`
	Current struct {
		Title string `json:"title"`
		ArtId int    `json:"art_id"`
	} `json:"current"`
	ItemType         string    `json:"item_type"`
	FreeDownloadPage string    `json:"freeDownloadPage"`
	ReleaseDate      timestamp `json:"album_release_date"`
	Trackinfo        []struct {
		Title    string `json:"title"`
		TrackNum int    `json:"track_num"`
		File     struct {
			Url string `json:"mp3-128"`
		} `json:"file"`
	} `json:"trackinfo"`
}

func downloadAlbum(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	doc, err := html.Parse(resp.Body)
	if err != nil {
		panic(err)
	}

	el := findElementsWithDataKey(doc, "data-tralbum")

	if el == nil {
		return fmt.Errorf("could not find album")
	}

	tralbumValue := getDataValue(el[0], "data-tralbum")

	var tralbum bandcampTRAlbum

	if err := json.Unmarshal([]byte(tralbumValue), &tralbum); err != nil {
		return fmt.Errorf("could not parse album JSON: %w", err)
	}

	if tralbum.FreeDownloadPage != "" {
		fmt.Println("This album is free to download in higher quality formats!")
		fmt.Printf("  %s\n", tralbum.FreeDownloadPage)
	}

	fmt.Println("Downloading", tralbum.Artist, tralbum.Current.Title, tralbum.ReleaseDate.Local().Format("2006"))

	// Fetch the album art, if one exists.
	var artBytes []byte
	var bigArtBytes []byte
	if tralbum.Current.ArtId != 0 {
		// Acquire a smaller one for embedding in id3.
		artUrl := fmt.Sprintf("https://f4.bcbits.com/img/a%d_16.jpg", tralbum.Current.ArtId)
		resp, err := http.Get(artUrl)
		if err != nil {
			return fmt.Errorf("could not fetch album art: %w", err)
		}
		defer resp.Body.Close()
		artBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			return errors.New("could not read album art")
		}

		// Get the full-sized one to store in the local dir.
		artUrl = fmt.Sprintf("https://f4.bcbits.com/img/a%d_0.jpg", tralbum.Current.ArtId)
		resp, err = http.Get(artUrl)
		if err != nil {
			return fmt.Errorf("could not fetch large album art: %w", err)
		}
		defer resp.Body.Close()
		bigArtBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			return errors.New("could not read large album art")
		}
	}

	// Create artist/album directory.
	albumPath := fmt.Sprintf("%s/%s (%s)", tralbum.Artist, tralbum.Current.Title, tralbum.ReleaseDate.Local().Format("2006"))

	if _, err := os.Stat(albumPath); os.IsNotExist(err) {
		if err := os.MkdirAll(albumPath, 0755); err != nil {
			return fmt.Errorf("could not create album directory: %w", err)
		}
	}

	// Save the large album art.
	if bigArtBytes != nil {
		artPath := fmt.Sprintf("%s/cover.jpg", albumPath)
		f, err := os.Create(artPath)
		if err != nil {
			return fmt.Errorf("could not create large album art file: %w", err)
		}
		if _, err := f.Write(bigArtBytes); err != nil {
			return fmt.Errorf("could not write large album art file: %w", err)
		}
		f.Close()
	}

	for _, track := range tralbum.Trackinfo {
		fmt.Printf(" %d %s ", track.TrackNum, track.Title)
		resp, err := http.Get(track.File.Url)
		if err != nil {
			return fmt.Errorf("could not fetch track: %w", err)
		}
		defer resp.Body.Close()

		trackPath := fmt.Sprintf("%s/%02d %s.mp3", albumPath, track.TrackNum, track.Title)

		f, err := os.Create(trackPath)
		if err != nil {
			return fmt.Errorf("could not create track file: %w", err)
		}

		_, err = f.ReadFrom(resp.Body)
		if err != nil {
			return fmt.Errorf("could not write track file: %w", err)
		}
		f.Close()

		// Add ID3 tags.
		tag, err := id3v2.Open(trackPath, id3v2.Options{Parse: true})
		if err != nil {
			return fmt.Errorf("could not open track file: %w", err)
		}
		defer tag.Close()

		tag.SetArtist(tralbum.Artist)
		tag.SetAlbum(tralbum.Current.Title)
		tag.SetYear(tralbum.ReleaseDate.Local().Format("2006"))
		tag.SetTitle(track.Title)
		tag.AddTextFrame("TRCK", id3v2.EncodingUTF8, fmt.Sprintf("%d", track.TrackNum))

		if artBytes != nil {
			pic := id3v2.PictureFrame{
				Encoding:    id3v2.EncodingUTF8,
				MimeType:    "image/jpeg",
				PictureType: id3v2.PTFrontCover,
				Description: "Front cover",
				Picture:     artBytes,
			}
			tag.AddAttachedPicture(pic)
		}

		if err := tag.Save(); err != nil {
			return fmt.Errorf("could not save track file: %w", err)
		}
		fmt.Printf("✔️ \n")
	}
	fmt.Println()
	return nil
}

func findElementsWithDataKey(n *html.Node, key string) []*html.Node {
	var results []*html.Node

	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == key {
				results = append(results, n)
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		results = append(results, findElementsWithDataKey(c, key)...)
	}

	return results
}

func getDataValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}

	return ""
}
