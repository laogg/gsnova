package helper

import (
	"bufio"
	"bytes"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

var letterRunes = []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

func RandAsciiString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

func GetRequestURLString(req *http.Request) string {
	if nil == req {
		return ""
	}
	str := req.URL.String()
	if len(req.URL.Scheme) == 0 && strings.EqualFold(req.Method, "Connect") && len(req.URL.Path) == 0 {
		str = fmt.Sprintf("https://%s", req.Host)
	}
	if !strings.HasPrefix(str, "http://") && !strings.HasPrefix(str, "https://") {
		scheme := req.URL.Scheme
		if len(req.URL.Scheme) == 0 {
			scheme = "http"

		}
		str = fmt.Sprintf("%s://%s%s", scheme, req.Host, str)
	}
	return str
}

func PrepareRegexp(rule string, only_star bool) (*regexp.Regexp, error) {
	rule = strings.TrimSpace(rule)
	rule = strings.Replace(rule, ".", "\\.", -1)
	if !only_star {
		rule = strings.Replace(rule, "?", "\\?", -1)
	}
	rule = strings.Replace(rule, "*", ".*", -1)
	return regexp.Compile(rule)
}

func WildcardMatch(text string, pattern string) bool {
	cards := strings.Split(pattern, "*")
	for _, str := range cards {
		idx := strings.Index(text, str)
		if idx == -1 {
			return false
		}
		text = strings.TrimLeft(text, str+"*")
	}
	return true
}

func ReadWithoutComment(file string, commentPrefix string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var total bytes.Buffer
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, commentPrefix) {
			total.WriteString(line)
			total.WriteString("\n")
		}
	}
	return total.Bytes(), nil
}
