package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"image/png"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chai2010/webp"
	"github.com/rjeczalik/notify"
	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/js"
	"github.com/tdewolff/minify/v2/json"
	"gopkg.in/kothar/brotli-go.v0/enc"
)

type Asset struct {
	Br, Data, WebP []byte
	Mime           string
}

var cssAssetURL = regexp.MustCompile(`asset-url\('(.*?)'\)`)
var cssSVGEmbed = regexp.MustCompile(`svg-embed\('(.*?)'(?:,(.*?))?\)`)
var cssVariable = regexp.MustCompile(`var\(--(.*?)\)`)
var jsAssetPath = regexp.MustCompile(`assetPath\('(.*?)'\)`)
var manifestSrc = regexp.MustCompile(`"src":"(.*?)"`)

var mimes = map[string]string{
	".css":         "text/css",
	".jpg":         "image/jpeg",
	".js":          "application/javascript",
	".png":         "image/png",
	".svg":         "image/svg+xml",
	".webmanifest": "application/manifest+json",
	".woff2":       "font/woff2",
}

var variables = map[string][]byte{
	"blue":         []byte("#007bff"),
	"green":        []byte("#28a745"),
	"green-dark":   []byte("#155724"),
	"green-light":  []byte("#d4edda"),
	"grey":         []byte("#ced4da"),
	"grey-dark":    []byte("#343a40"),
	"grey-light":   []byte("#f8f9fa"),
	"red":          []byte("#dc3545"),
	"red-dark":     []byte("#721c24"),
	"red-light":    []byte("#f8d7da"),
	"yellow":       []byte("#ffc107"),
	"yellow-light": []byte("#fff3cd"),
	"yellow-dark":  []byte("#856404"),
}

var assets map[string]Asset
var dev = os.Getenv("DEV") == "1"
var min = minify.New()
var paths map[string]string

func cssAssetURLFunc(match []byte) []byte {
	file := cssAssetURL.FindStringSubmatch(string(match))[1]
	return []byte("url(" + paths[file] + ")")
}

func cssSVGEmbedFunc(match []byte) []byte {
	matches := cssSVGEmbed.FindStringSubmatch(string(match))

	svg, err := ioutil.ReadFile(matches[1])
	if err != nil {
		panic(err)
	}

	if matches[2] != "" {
		svg = bytes.ReplaceAll(svg, []byte("FILL"), []byte(matches[2]))
	}

	svg = cssVariable.ReplaceAllFunc(svg, cssVariableFunc)
	svg = bytes.ReplaceAll(svg, []byte{'"'}, []byte{'\''})
	svg = bytes.ReplaceAll(svg, []byte{'#'}, []byte("%23"))

	return append([]byte(`url("data:image/svg+xml,`), append(svg, '"', ')')...)
}

func cssVariableFunc(match []byte) []byte {
	return variables[cssVariable.FindStringSubmatch(string(match))[1]]
}

func jsAssetPathFunc(match []byte) []byte {
	file := jsAssetPath.FindStringSubmatch(string(match))[1]
	return []byte("'" + paths[file] + "'")
}

func manifestSrcFunc(match []byte) []byte {
	file := manifestSrc.FindStringSubmatch(string(match))[1]
	return []byte(`"src":"` + paths[file] + `"`)
}

func main() {
	min.AddFunc("application/javascript", js.Minify)
	min.AddFunc("application/manifest+json", json.Minify)
	min.AddFunc("text/css", css.Minify)

	if err := os.Chdir("assets"); err != nil {
		panic(err)
	}

	run()

	if dev {
		c := make(chan notify.EventInfo, 1)

		if err := notify.Watch("./...", c, notify.All); err != nil {
			panic(err)
		}

		var lastRun time.Time

		for {
			event := <-c

			// Very crude debouncing.
			if time.Since(lastRun) > time.Millisecond {
				fmt.Println(event)
				run()
			}

			lastRun = time.Now()
		}
	}
}

func run() {
	start := time.Now()

	var files []string

	assets = map[string]Asset{}
	paths = map[string]string{}

	if err := filepath.Walk(".", func(file string, fi os.FileInfo, err error) error {
		if !fi.IsDir() && !strings.HasPrefix(path.Base(file), "_") {
			files = append(files, file)
		}

		return err
	}); err != nil {
		panic(err)
	}

	// Process images first because they could be referenced in other assets.
	sort.Slice(files, func(i, j int) bool {
		iImg := strings.HasPrefix(files[i], "images/")
		jImg := strings.HasPrefix(files[j], "images/")

		if iImg != jImg {
			return iImg
		}

		iServiceWorker := strings.HasSuffix(files[i], "service-worker.js")
		jServiceWorker := strings.HasSuffix(files[j], "service-worker.js")

		if iServiceWorker != jServiceWorker {
			return iServiceWorker
		}

		return files[i] < files[j]
	})

	for _, file := range files {
		ext := filepath.Ext(file)

		if ext == ".go" {
			continue
		}

		asset := Asset{Mime: mimes[ext]}

		if asset.Mime == "" {
			panic("Unsupported: " + file)
		}

		var err error
		if asset.Data, err = ioutil.ReadFile(file); err != nil {
			panic(err)
		}

		switch ext {
		case ".css":
			asset.Data = cssAssetURL.ReplaceAllFunc(asset.Data, cssAssetURLFunc)
			asset.Data = cssVariable.ReplaceAllFunc(asset.Data, cssVariableFunc)
			fallthrough
		case ".js", ".webmanifest":
			asset.Data = jsAssetPath.ReplaceAllFunc(asset.Data, jsAssetPathFunc)

			// Minify
			if asset.Data, err = min.Bytes(asset.Mime, asset.Data); err != nil {
				panic(err)
			}

			switch ext {
			case ".css":
				// FIXME After minify because https://github.com/tdewolff/minify/issues/180
				asset.Data = cssSVGEmbed.ReplaceAllFunc(asset.Data, cssSVGEmbedFunc)
			case ".webmanifest":
				asset.Data = manifestSrc.ReplaceAllFunc(asset.Data, manifestSrcFunc)
			}
		case ".svg":
			asset.Data = cssVariable.ReplaceAllFunc(asset.Data, cssVariableFunc)
		}

		if !dev {
			// Brotli or WebP
			switch ext {
			case ".css", ".js", ".svg":
				if asset.Br, err = enc.CompressBuffer(nil, asset.Data, nil); err != nil {
					panic(err)
				}
			case ".png":
				img, err := png.Decode(bytes.NewBuffer(asset.Data))
				if err != nil {
					panic(err)
				}

				var buf bytes.Buffer
				if err := webp.Encode(&buf, img, &webp.Options{Lossless: true}); err != nil {
					panic(err)
				}

				asset.WebP = buf.Bytes()
			}
		}

		hash := md5.Sum(asset.Data)
		fingerprint := base64.RawURLEncoding.EncodeToString(hash[:])

		assets[fingerprint] = asset
		paths[file] = "/assets/" + fingerprint
	}

	file, err := os.Create("assets.go")
	if err != nil {
		panic(err)
	}

	if _, err := file.WriteString("package assets;var Paths=map[string]string{"); err != nil {
		panic(err)
	}

	for path, hash := range paths {
		if _, err := fmt.Fprintf(file, "%#v:%#v,", path, hash); err != nil {
			panic(err)
		}
	}

	if _, err := file.WriteString(
		"};var Assets=map[string]struct{Br,Data,WebP[]byte;Mime string}{",
	); err != nil {
		panic(err)
	}

	for hash, asset := range assets {
		if _, err := fmt.Fprintf(
			file,
			"%#v:{[]byte(%#v),[]byte(%#v),[]byte(%#v),%#v},",
			hash,
			string(asset.Br),
			string(asset.Data),
			string(asset.WebP),
			asset.Mime,
		); err != nil {
			panic(err)
		}
	}

	if _, err := file.WriteString("}"); err != nil {
		panic(err)
	}

	if err := file.Close(); err != nil {
		panic(err)
	}

	fmt.Println("Processed", len(files), "assets in", time.Since(start))
}
