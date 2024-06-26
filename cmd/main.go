package main

import (
	_ "embed"

	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"
)

//go:embed base.html
var baseTplString string

//go:embed index.html
var indexTplStr string

//go:embed login.html
var loginTplString string

//go:embed status.html
var statusTplString string

var (
	indexTpl  *template.Template
	baseTpl   *template.Template
	loginTpl  *template.Template
	statusTpl *template.Template
)

func mustTpl(page, content string) *template.Template {
	var sb strings.Builder
	if err := baseTpl.Execute(&sb, content); err != nil {
		panic(err)
	}
	return template.Must(template.New(page).Parse(sb.String()))
}

func init() {
	baseTpl = template.Must(template.New("base.html").Parse(baseTplString))
	indexTpl = mustTpl("index.html", indexTplStr)
	//loginTpl = mustTpl("login.html", loginTplString)
	statusTpl = mustTpl("status.html", statusTplString)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

var Option struct {
	Addr         string
	BBDown       string
	Download     string
	User         string
	Action       string
	Password     string
//	BBDownConfig string
	Mimetype     string
	Quality	     string
}

func init() {
	flag.StringVar(&Option.Addr, "addr", ":9270", "http server listen address")
	var defaultBBDown = "youtubedr"
	if _, err := os.Stat("./youtubedr"); err == nil {
		defaultBBDown = "./youtubedr"
	}
	flag.StringVar(&Option.BBDown, "bbdown", defaultBBDown, "youtubedr path")
	flag.StringVar(&Option.Action, "action", "", "download action")
	flag.StringVar(&Option.Download, "download", "./", "download path")
	flag.StringVar(&Option.Quality, "quality", "hd1080", "quality parameter")
	flag.StringVar(&Option.Mimetype, "mimetype", "mp4", "mimetype parameter")
	//flag.StringVar(&Option.BBDownConfig, "bbdown-config", "", "youtubedr config file")
	Option.User = os.Getenv("AUTH_USER")
	Option.Password = os.Getenv("AUTH_PWD")
}

func main() {
	flag.Parse()
	var s Service
	log.Println("serve at", Option.Addr)
	if err := s.Serve(Option.Addr); err != nil {
		log.Fatal(err)
	}
}

func format(s interface{}) string {
	var bs strings.Builder
	enc := json.NewEncoder(&bs)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bs.String()
}

type Job struct {
	URL       string
	EscapeURL string // for render index.html
	Start     time.Time
	Spend     time.Duration
	Cmd       *Cmd
	State     string
}

type Cmd struct {
	Cmd    *exec.Cmd
	Output *os.File
}

func Exec(name string, args ...string) (*Cmd, error) {
	file, err := os.CreateTemp("", "bbdown-*")
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(name, args...)
	cmd.Stdout = file
	cmd.Stderr = file
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Cmd{Cmd: cmd, Output: file}, nil
}

const maxLogSize = 1 << 20

func (c *Cmd) Tail() ([]byte, error) {
	if c.Output == nil {
		return nil, fmt.Errorf("cmd is closed")
	}
	offset, err := c.Output.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	if offset == 0 {
		return nil, nil
	}

	size := offset
	if size > maxLogSize {
		size = maxLogSize
	}
	var resp = make([]byte, size)
	start := offset - int64(len(resp))
	if start <= 0 {
		start = 0
	}
	if _, err := c.Output.ReadAt(resp, start); err != nil && err != io.EOF {
		return nil, err
	}
	return bytes.ToValidUTF8(resp, nil), nil
}

func (c *Cmd) Close() {
	if c.Cmd == nil {
		return
	}
	if c.Cmd.Process != nil {
		c.Cmd.Process.Kill()
	}
	c.Cmd.Wait()
	c.Output.Close()
	os.Remove(c.Output.Name())
	c.Output = nil
	c.Cmd = nil
}

// Start a download job
func Start(joburl string) (*Job, error) {
	var j Job
	j.URL = joburl
	j.EscapeURL = url.QueryEscape(j.URL)
	j.Start = time.Now()
	var opts = []string{
		"download",
		//"",
	}
	if Option.Download != "" {
		opts = append(opts, "--directory", Option.Download)
	}
	if Option.Quality != "" {
		opts = append(opts, "--quality", Option.Quality)
	}
	if Option.Mimetype != "" {
		opts = append(opts, "--mimetype", Option.Mimetype)
	}
	opts = append(opts, joburl)
	cmd, err := Exec(Option.BBDown, opts...)
	if err != nil {
		return nil, err
	}
	j.Cmd = cmd
	go func() {
		log.Println(j.URL, "started", j.Cmd.Output.Name())
		if err := j.Cmd.Cmd.Wait(); err != nil {
			log.Println(j.URL, "fails", err, j.Cmd.Output.Name())
		} else {
			log.Println(j.URL, "finish", j.Cmd.Output.Name())
		}
	}()
	return &j, nil
}

type Data struct {
	Alerts []string
	Jobs   []*Job
}

type sortJob []*Job

func (s sortJob) Len() int {
	return len(s)
}

func (s sortJob) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s sortJob) Less(i, j int) bool {
	return s[i].Start.Before(s[j].Start)
}

func sortJobs(jobs []*Job) {
	sort.Sort(sortJob(jobs))
}

type Service struct {
	mu       sync.Mutex
	mux      *http.ServeMux
	Jobs     map[string]*Job
	alertsmu sync.Mutex
	Alerts   []string
}

func (s *Service) Index(w http.ResponseWriter, r *http.Request) {
	log.Println(r.Method, r.URL.String())
	var d Data
	d.Jobs = s.jobs()
	for _, j := range d.Jobs {
		j.Spend = time.Since(j.Start)
	}
	d.Alerts = s.alerts()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := indexTpl.Execute(w, d); err != nil {
		log.Println(err)
	}
}

func (s *Service) Submit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	url := strings.TrimSpace(r.Form.Get("url"))
	if url != "" {
		log.Println("Add new job", url)
		s.submitJob(url)
	}
	w.Header().Add("Location", "/")
	w.WriteHeader(303)
}

func (s *Service) Status(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	url := strings.TrimSpace(r.Form.Get("job"))
	s.mu.Lock()
	j := s.Jobs[url]
	s.mu.Unlock()
	if j == nil {
		w.WriteHeader(404)
		return
	}
	resp, err := j.Cmd.Tail()
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintln(w, err)
		return
	}
	cmd := j.Cmd
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if cmd.Cmd.ProcessState != nil && cmd.Cmd.ProcessState.Exited() {
		resp = append(resp, '\n')
		resp = append(resp, cmd.Cmd.ProcessState.String()...)
	}
	if err := statusTpl.Execute(w, string(resp)); err != nil {
		log.Println(err)
	}
}

func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	url := strings.TrimSpace(r.Form.Get("job"))
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.Jobs[url]
	delete(s.Jobs, url)
	if j != nil {
		j.Cmd.Close()
	}
	w.Header().Add("Location", "/")
	w.WriteHeader(303)
}

var (
	loginMu  sync.Mutex
	loginCmd *Cmd
)

func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	loginMu.Lock()
	defer loginMu.Unlock()

	if loginCmd != nil {
		loginCmd.Close()
	}

	os.Remove("./qrcode.png")
	cmd, err := Exec(Option.BBDown, "login")
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintln(w, err)
		return
	}
	loginCmd = cmd
	defer func() {
		go func() {
			go func() {
				err := cmd.Cmd.Wait()
				log.Println("login return with", err)
			}()
			time.Sleep(time.Second * 60)
			cmd.Close()
		}()
	}()

	time.Sleep(time.Second)

	file, err := os.Open("./qrcode.png")
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintln(w, err)
		return
	}
	var image strings.Builder
	enc := base64.NewEncoder(base64.StdEncoding, &image)
	if _, err := io.Copy(enc, file); err != nil {
		w.WriteHeader(500)
		fmt.Fprintln(w, err)
		return
	}
	enc.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := loginTpl.Execute(w, image.String()); err != nil {
		log.Println(err)
	}
}

func (s *Service) LoginLog(w http.ResponseWriter, r *http.Request) {
	loginMu.Lock()
	cmd := loginCmd
	loginMu.Unlock()
	if cmd == nil {
		w.WriteHeader(200)
		fmt.Fprintln(w, "process not exists")
		return
	}
	data, err := cmd.Tail()
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintln(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintln(w, string(bytes.ReplaceAll(data, []byte("█"), nil)))
	if cmd.Cmd.ProcessState != nil && cmd.Cmd.ProcessState.Exited() {
		fmt.Fprintln(w, cmd.Cmd.ProcessState.String())
	}
}

func (s *Service) Version(w http.ResponseWriter, r *http.Request) {
	cmd, err := Exec(Option.BBDown, "version")
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintln(w, err)
		return
	}
	cmd.Cmd.Wait()
	resp, err := cmd.Tail()
	if err != nil {
		w.WriteHeader(500)
		fmt.Fprintln(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := statusTpl.Execute(w, string(resp)); err != nil {
		log.Println(err)
	}
}

func (s *Service) ServeFile(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimLeft(r.URL.Path, "/files")
	name := Option.Download + "/" + path
	log.Println("GET file", name)
	http.ServeFile(w, r, name)
}

func (s *Service) Ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(200)
	w.Write([]byte("OK"))
}

func (s *Service) addAlerts(t string) {
	s.alertsmu.Lock()
	s.Alerts = append(s.Alerts, t)
	s.alertsmu.Unlock()
}

func (s *Service) alerts() []string {
	s.alertsmu.Lock()
	a := s.Alerts
	s.Alerts = nil
	s.alertsmu.Unlock()
	return a
}

func (s *Service) submitJob(url string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	if j, ok := s.Jobs[url]; ok {
		s.addAlerts(fmt.Sprintf("url exists %s", url))
		return j
	}
	j, err := Start(url)
	if err != nil {
		s.addAlerts(fmt.Sprintf("url(%s) fails: %v", url, err))
		return nil
	}

	if s.Jobs == nil {
		s.Jobs = map[string]*Job{}
	}
	s.Jobs[url] = j
	return j
}

func (s *Service) jobs() []*Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	var i int
	result := make([]*Job, len(s.Jobs))
	for _, v := range s.Jobs {
		if v.Cmd.Cmd.ProcessState != nil {
			v.State = v.Cmd.Cmd.ProcessState.String()
		} else {
			v.State = "running"
		}
		result[i] = v
		i++
	}
	sortJobs(result)
	return result
}

func (s *Service) Handle(method, path string, h func(w http.ResponseWriter, r *http.Request)) {
	s.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if r.Method != method {
			w.WriteHeader(405)
			return
		}
		log.Println(r.Method, r.URL)
		if Option.User != "" {
			user, pass, ok := r.BasicAuth()
			if !ok ||
				subtle.ConstantTimeCompare([]byte(user), []byte(Option.User)) != 1 ||
				subtle.ConstantTimeCompare([]byte(pass), []byte(Option.Password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="bbdown"`)
				w.WriteHeader(401)
				w.Write([]byte("Unauthorised.\n"))
				return
			}
		}
		h(w, r)
	})
}

func (s *Service) Serve(addr string) error {
	if s.mux == nil {
		s.mux = http.NewServeMux()
	}
	s.Handle("GET", "/", s.Index)
	s.Handle("POST", "/jobs/submit", s.Submit)
	s.Handle("GET", "/jobs/status", s.Status)
	s.Handle("GET", "/jobs/delete", s.Delete)
	s.Handle("GET", "/login", s.Login)
	s.Handle("GET", "/login/log", s.LoginLog)
	s.Handle("GET", "/ping", s.Ping)
	//s.Handle("GET", "/files/", s.ServeFile)
	s.Handle("GET", "/version", s.Version)
	return http.ListenAndServe(addr, s.mux)
}
