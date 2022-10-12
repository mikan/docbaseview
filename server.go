/*
DocBase のエクスポートファイルを閲覧するサーバーを実行します。

Usage:

	docbaseview [flags]

The flags are:

	-p
		リッスンする TCP ポートを指定します。デフォルトは 8080 です。環境変数 PORT がある場合はそちらを優先します。
	-m
		エクスポートした Markdown ファイルのディレクトリを指定します。デフォルトは md です。
	-i
		エクスポートした画像ファイルのディレクトリを指定します。デフォルトは img です。
	-f
		エクスポートしたその他ファイルのディレクトリを指定します。デフォルトは file です。
	-bu
		Basic 認証のユーザー名を指定します。省略すると Basic 認証を無効にします。
	-bp
		Basic 認証のパスワードを指定します。
*/
package main

import (
	"bufio"
	_ "embed"
	"flag"
	"html/template"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/parser"
)

var (
	//go:embed index.gohtml
	indexHTML []byte
	//go:embed doc.gohtml
	docHTML []byte
	//go:embed doc.css
	docCSS []byte

	indexTemplate, documentTemplate *template.Template
	basicUser, basicPassword        string
	mdDir, imgDir, fileDir          string
	mdEntries                       []document

	imgLinkToNameMap  = make(map[string]string)
	fileLinkToNameMap = make(map[string]string)
	mdLinkPattern     = regexp.MustCompile(`#{([0-9]+)}`)
	fileLinkPattern   = regexp.MustCompile(`https://docbase\.io/file_attachments/([0-9a-zA-Z.]+)`)
	fileIconPattern   = regexp.MustCompile(`!\[[a-z]+]\(/images/file_icons/[a-z]+\.svg\)`)
	imgLinkPattern    = regexp.MustCompile(`https://image\.docbase\.io/uploads/([0-9a-zA-Z-.]+)[^)]*`)
)

type document struct {
	FileName string
	Title    string
}

func main() {
	port := flag.Int("p", 8080, "port to listen")
	flag.StringVar(&basicUser, "bu", "", "user of the basic auth, empty to disable")
	flag.StringVar(&basicPassword, "bp", "", "password of the basic auth")
	flag.StringVar(&mdDir, "m", "md", "directory of the exported markdown files")
	flag.StringVar(&imgDir, "i", "img", "directory of the exported images")
	flag.StringVar(&fileDir, "f", "file", "directory of the exported files")
	flag.Parse()
	if sp := os.Getenv("PORT"); len(sp) > 0 {
		if p, err := strconv.Atoi(sp); err == nil {
			*port = p
		}
	}

	// scan md dir
	mdDirEntries, err := os.ReadDir(mdDir)
	if err != nil {
		log.Fatalf("failed to read markdown directory %s: %v", mdDir, err)
	}
	for _, entry := range mdDirEntries {
		if !entry.IsDir() {
			e := document{FileName: entry.Name()}
			if e.Title, err = head(path.Join(mdDir, entry.Name())); err != nil {
				log.Printf("failed to read title of %s: %v", path.Join(mdDir, entry.Name()), err)
			}
			mdEntries = append(mdEntries, e)
		}
	}

	// scan img dir
	imgDirEntries, err := os.ReadDir(imgDir)
	if err != nil {
		log.Fatalf("failed to read images directory %s: %v", imgDir, err)
	}
	for _, entry := range imgDirEntries {
		if !entry.IsDir() {
			imgLinkToNameMap[entry.Name()[strings.LastIndex(entry.Name(), "_")+1:]] = entry.Name()
		}
	}

	// scan file dir
	fileDirEntries, err := os.ReadDir(fileDir)
	if err != nil {
		log.Fatalf("failed to read files directory %s: %v", fileDir, err)
	}
	for _, entry := range fileDirEntries {
		if !entry.IsDir() {
			fileLinkToNameMap[entry.Name()[strings.LastIndex(entry.Name(), "_")+1:]] = entry.Name()
		}
	}

	// create template
	indexTemplate = template.Must(template.New("index").Parse(string(indexHTML)))
	documentTemplate = template.Must(template.New("document").Parse(string(docHTML)))

	// start the server
	http.HandleFunc("/", catchAll)
	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	http.HandleFunc("/doc.css", func(w http.ResponseWriter, r *http.Request) { write(w, r, docCSS, "text/css") })
	log.Printf("server listening on port %d", *port)
	if err := http.ListenAndServe(":"+strconv.Itoa(*port), nil); err != nil {
		log.Fatalf("server terminated: %v", err)
	}
}

func catchAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if len(basicUser) > 0 {
		if id, secret, ok := r.BasicAuth(); !ok || id != basicUser || secret != basicPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="ログインしてください"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			log.Printf("[%s] HTTP %d", r.RequestURI, http.StatusUnauthorized)
			return
		}
	}
	fileName := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case len(fileName) == 0:
		handleIndex(w, r)
	case strings.HasSuffix(strings.ToLower(fileName), ".md"):
		handleMarkdown(w, r, fileName)
	case strings.HasSuffix(strings.ToLower(fileName), ".jpg"):
		fallthrough
	case strings.HasSuffix(strings.ToLower(fileName), ".jpeg"):
		fallthrough
	case strings.HasSuffix(strings.ToLower(fileName), ".png"):
		fallthrough
	case strings.HasSuffix(strings.ToLower(fileName), ".gif"):
		handleImage(w, r, fileName)
	default:
		handleFile(w, r, fileName)
	}
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if err := indexTemplate.Execute(w, map[string]any{"Documents": mdEntries}); err != nil {
		log.Printf("[%s] failed to write response: %v", r.RequestURI, err)
		return
	}
	log.Printf("[%s] HTTP %d", r.RequestURI, http.StatusOK)
}

func handleMarkdown(w http.ResponseWriter, r *http.Request, fileName string) {
	filePath := path.Join(mdDir, fileName)
	if _, err := os.Stat(filePath); err != nil {
		http.NotFound(w, r)
		log.Printf("[%s] HTTP %d", r.RequestURI, http.StatusNotFound)
		return
	}
	title, content, err := headAndContent(filePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Printf("[%s] HTTP %d failed to read %s: %v", r.RequestURI, http.StatusInternalServerError, filePath, err)
		return
	}
	mdParser := parser.NewWithExtensions(parser.CommonExtensions | parser.AutoHeadingIDs)
	htmlContent := markdown.ToHTML(fixEmoji(fixLinks([]byte(content))), mdParser, nil)
	if err = documentTemplate.Execute(w, map[string]any{"Title": title, "HTMLContent": template.HTML(htmlContent)}); err != nil {
		log.Printf("[%s] failed to write response: %v", r.RequestURI, err)
		return
	}
	log.Printf("[%s] HTTP %d", r.RequestURI, http.StatusOK)
}

func handleImage(w http.ResponseWriter, r *http.Request, fileName string) {
	actualImageName, ok := imgLinkToNameMap[fileName]
	if !ok {
		http.NotFound(w, r)
		log.Printf("[%s] HTTP %d", r.RequestURI, http.StatusNotFound)
		return
	}
	imgPath := path.Join(imgDir, actualImageName)
	content, err := os.ReadFile(imgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Printf("[%s] HTTP %d failed to read %s: %v", r.RequestURI, http.StatusInternalServerError, imgPath, err)
		return
	}
	write(w, r, content, http.DetectContentType(content))
}

func handleFile(w http.ResponseWriter, r *http.Request, fileName string) {
	actualFileName, ok := fileLinkToNameMap[fileName]
	if !ok {
		http.NotFound(w, r)
		log.Printf("[%s] HTTP %d", r.RequestURI, http.StatusNotFound)
		return
	}
	filePath := path.Join(fileDir, actualFileName)
	content, err := os.ReadFile(filePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Printf("[%s] HTTP %d failed to read %s: %v", r.RequestURI, http.StatusInternalServerError, filePath, err)
		return
	}
	write(w, r, content, http.DetectContentType(content))
}

func write(w http.ResponseWriter, r *http.Request, content []byte, contentType string) {
	w.Header().Set("Content-Type", contentType)
	if _, err := w.Write(content); err != nil {
		log.Printf("[%s] failed to write response: %v", r.RequestURI, err)
	}
	log.Printf("[%s] HTTP %d", r.RequestURI, http.StatusOK)
}

func head(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer func(f *os.File) {
		if err := f.Close(); err != nil {
			log.Printf("failed to close %s: %v", filePath, err)
		}
	}(f)
	scanner := bufio.NewScanner(f)
	if scanner.Scan() {
		return scanner.Text(), nil
	}
	return "", scanner.Err()
}

func headAndContent(filePath string) (head, content string, err error) {
	var f *os.File
	f, err = os.Open(filePath)
	if err != nil {
		return
	}
	defer func() { err = f.Close() }()
	scanner := bufio.NewScanner(f)
	if scanner.Scan() {
		head = scanner.Text()
	}
	for scanner.Scan() {
		content += scanner.Text() + "\n"
	}
	err = scanner.Err()
	return
}

func fixLinks(input []byte) []byte {
	s := string(input)
	s = mdLinkPattern.ReplaceAllString(s, `🔗 <a href="$1.md">$1.md</a>`)
	s = fileLinkPattern.ReplaceAllString(s, "$1")
	s = fileIconPattern.ReplaceAllString(s, "📄️")
	s = imgLinkPattern.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "[ ]", `<input type="checkbox" disabled></input>`)
	s = strings.ReplaceAll(s, "[x]", `<input type="checkbox" disabled checked></input>`)
	s = strings.ReplaceAll(s, "/guidance/", "https://help.docbase.io/guidance/")
	return []byte(s)
}

// emojiDict は絵文字の辞書です。今のところよく使うものだけ対応します。
var emojiDict = map[string]string{
	"+1":             "👍",
	"-1":             "👎",
	"bulb":           "💡",
	"computer":       "💻",
	"inbox_tray":     "📥",
	"link":           "🔗",
	"lock":           "🔒",
	"mag":            "🔍",
	"memo":           "📝",
	"moneybag":       "💰",
	"movie_camera":   "🎥",
	"poop":           "💩",
	"pray":           "🙏",
	"shit":           "💩",
	"sparkle":        "✨",
	"sparkles":       "✨",
	"speech_balloon": "💬",
	"unlock":         "🔓",
}

func fixEmoji(input []byte) []byte {
	s := string(input)
	for k, v := range emojiDict {
		s = strings.ReplaceAll(s, ":"+k+":", v)
	}
	return []byte(s)
}
