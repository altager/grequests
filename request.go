package grequests

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-querystring/query"

	"context"

	"golang.org/x/net/publicsuffix"
)

// RequestOptions is the location that of where the data
type RequestOptions struct {

	// Data is a map of key values that will eventually convert into the
	// the body of a POST request.
	Data map[string]string

	// Params is a map of query strings that may be used within a GET request
	Params map[string]string

	// QueryStruct is a struct that encapsulates a set of URL query params
	// this paramter is mutually exclusive with `Params map[string]string` (they cannot be combined)
	// for more information please see https://godoc.org/github.com/google/go-querystring/query
	QueryStruct interface{}

	// Files is where you can include files to upload. The use of this data
	// structure is limited to POST requests
	Files []FileUpload

	// JSON can be used when you wish to send JSON within the request body
	JSON interface{}

	// XML can be used if you wish to send XML within the request body
	XML interface{}

	// Headers if you want to add custom HTTP headers to the request,
	// this is your friend
	Headers map[string]string

	// InsecureSkipVerify is a flag that specifies if we should validate the
	// server's TLS certificate. It should be noted that Go's TLS verify mechanism
	// doesn't validate if a certificate has been revoked
	InsecureSkipVerify bool

	// DisableCompression will disable gzip compression on requests
	DisableCompression bool

	// UserAgent allows you to set an arbitrary custom user agent
	UserAgent string

	// Host allows you to set an arbitrary custom host
	Host string

	// Auth allows you to specify a user name and password that you wish to
	// use when requesting the URL. It will use basic HTTP authentication
	// formatting the username and password in base64 the format is:
	// []string{username, password}
	Auth []string

	// IsAjax is a flag that can be set to make the request appear
	// to be generated by browser Javascript
	IsAjax bool

	// Cookies is an array of `http.Cookie` that allows you to attach
	// cookies to your request
	Cookies []*http.Cookie

	// UseCookieJar will create a custom HTTP client that will
	// process and store HTTP cookies when they are sent down
	UseCookieJar bool

	// Proxies is a map in the following format
	// *protocol* => proxy address e.g http => http://127.0.0.1:8080
	Proxies map[string]*url.URL

	// TLSHandshakeTimeout specifies the maximum amount of time waiting to
	// wait for a TLS handshake. Zero means no timeout.
	TLSHandshakeTimeout time.Duration

	// DialTimeout is the maximum amount of time a dial will wait for
	// a connect to complete.
	DialTimeout time.Duration

	// KeepAlive specifies the keep-alive period for an active
	// network connection. If zero, keep-alive are not enabled.
	DialKeepAlive time.Duration

	// RequestTimeout is the maximum amount of time a whole request(include dial / request / redirect)
	// will wait.
	RequestTimeout time.Duration

	// HTTPClient can be provided if you wish to supply a custom HTTP client
	// this is useful if you want to use an OAUTH client with your request.
	HTTPClient *http.Client

	// SensitiveHTTPHeaders is a map of sensitive HTTP headers that a user
	// doesn't want passed on a redirect.
	SensitiveHTTPHeaders map[string]struct{}

	// RedirectLimit is the acceptable amount of redirects that we should expect
	// before returning an error be default this is set to 30. You can change this
	// globally by modifying the `RedirectLimit` variable.
	RedirectLimit int

	// Set false to check response before redirect
	AllowRedirect bool

	// RequestBody allows you to put anything matching an `io.Reader` into the request
	// this option will take precedence over any other request option specified
	RequestBody io.Reader

	// CookieJar allows you to specify a special cookiejar to use with your request.
	// this option will take precedence over the `UseCookieJar` option above.
	CookieJar http.CookieJar

	// Context can be used to maintain state between requests https://golang.org/pkg/context/#Context
	Context context.Context

	// BeforeRequest is a hook that can be used to modify the request object
	// before the request has been fired. This is useful for adding authentication
	// and other functionality not provided in this library
	BeforeRequest func(req *http.Request) error

	// LocalAddr allows you to send the request on any local interface
	LocalAddr *net.TCPAddr
}

// DoRegularRequest adds generic test functionality
func DoRegularRequest(requestVerb, url string, ro *RequestOptions) (*Response, error) {
	return buildResponse(buildRequest(requestVerb, url, ro, nil))
}

func doSessionRequest(requestVerb, url string, ro *RequestOptions, httpClient *http.Client) (*Response, error) {
	return buildResponse(buildRequest(requestVerb, url, ro, httpClient))
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

func escapeQuotes(s string) string {
	return quoteEscaper.Replace(s)
}

// buildRequest is where most of the magic happens for request processing
func buildRequest(httpMethod, url string, ro *RequestOptions, httpClient *http.Client) (*http.Response, error) {
	if ro == nil {
		ro = &RequestOptions{}
	}

	if ro.CookieJar != nil {
		ro.UseCookieJar = true
	}

	// Create our own HTTP client

	if httpClient == nil {
		httpClient = BuildHTTPClient(*ro)
	}

	var err error // we don't want to shadow url so we won't use :=
	switch {
	case len(ro.Params) != 0:
		if url, err = buildURLParams(url, ro.Params); err != nil {
			return nil, err
		}
	case ro.QueryStruct != nil:
		if url, err = buildURLStruct(url, ro.QueryStruct); err != nil {
			return nil, err
		}
	}

	// Build the request
	req, err := buildHTTPRequest(httpMethod, url, ro)

	if err != nil {
		return nil, err
	}

	// Do we need to add any HTTP headers or Basic Auth?
	addHTTPHeaders(ro, req)
	addCookies(ro, req)

	addRedirectFunctionality(httpClient, ro)

	if ro.Context != nil {
		req = req.WithContext(ro.Context)
	}

	if ro.BeforeRequest != nil {
		if err := ro.BeforeRequest(req); err != nil {
			return nil, err
		}
	}

	return httpClient.Do(req)
}

func buildHTTPRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {
	if ro.RequestBody != nil {
		return http.NewRequest(httpMethod, userURL, ro.RequestBody)
	}

	if ro.JSON != nil {
		return createBasicJSONRequest(httpMethod, userURL, ro)
	}

	if ro.XML != nil {
		return createBasicXMLRequest(httpMethod, userURL, ro)
	}

	if ro.Files != nil {
		return createFileUploadRequest(httpMethod, userURL, ro)
	}

	if ro.Data != nil {
		return createBasicRequest(httpMethod, userURL, ro)
	}

	return http.NewRequest(httpMethod, userURL, nil)
}

func createFileUploadRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {
	if httpMethod == "POST" {
		return createMultiPartPostRequest(httpMethod, userURL, ro)
	}

	// This may be a PUT or PATCH request so we will just put the raw
	// io.ReadCloser in the request body
	// and guess the MIME type from the file name

	// At the moment, we will only support 1 file upload as a time
	// when uploading using PUT or PATCH

	req, err := http.NewRequest(httpMethod, userURL, ro.Files[0].FileContents)

	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", mime.TypeByExtension(ro.Files[0].FileName))

	return req, nil

}

func createBasicXMLRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {
	var reader io.Reader

	switch ro.XML.(type) {
	case string:
		reader = strings.NewReader(ro.XML.(string))
	case []byte:
		reader = bytes.NewReader(ro.XML.([]byte))
	default:
		byteSlice, err := xml.Marshal(ro.XML)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(byteSlice)
	}

	req, err := http.NewRequest(httpMethod, userURL, reader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/xml")

	return req, nil

}
func createMultiPartPostRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {
	requestBody := &bytes.Buffer{}

	multipartWriter := multipart.NewWriter(requestBody)

	for i, f := range ro.Files {

		if f.FileContents == nil {
			return nil, errors.New("grequests: Pointer FileContents cannot be nil")
		}

		fieldName := f.FieldName

		if fieldName == "" {
			if len(ro.Files) > 1 {
				fieldName = strings.Join([]string{"file", strconv.Itoa(i + 1)}, "")
			} else {
				fieldName = "file"
			}
		}

		var writer io.Writer
		var err error

		if f.FileMime != "" {
			if f.FileName == "" {
				f.FileName = "filename"
			}
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(fieldName), escapeQuotes(f.FileName)))
			h.Set("Content-Type", f.FileMime)
			writer, err = multipartWriter.CreatePart(h)
		} else {
			writer, err = multipartWriter.CreateFormFile(fieldName, f.FileName)
		}

		if err != nil {
			return nil, err
		}

		if _, err = io.Copy(writer, f.FileContents); err != nil && err != io.EOF {
			return nil, err
		}

		if err := f.FileContents.Close(); err != nil {
			return nil, err
		}

	}

	// Populate the other parts of the form (if there are any)
	for key, value := range ro.Data {
		multipartWriter.WriteField(key, value)
	}

	if err := multipartWriter.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(httpMethod, userURL, requestBody)

	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", multipartWriter.FormDataContentType())

	return req, err
}

func createBasicJSONRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {

	var reader io.Reader
	switch ro.JSON.(type) {
	case string:
		reader = strings.NewReader(ro.JSON.(string))
	case []byte:
		reader = bytes.NewReader(ro.JSON.([]byte))
	default:
		byteSlice, err := json.Marshal(ro.JSON)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(byteSlice)
	}

	req, err := http.NewRequest(httpMethod, userURL, reader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	return req, nil

}
func createBasicRequest(httpMethod, userURL string, ro *RequestOptions) (*http.Request, error) {

	req, err := http.NewRequest(httpMethod, userURL, strings.NewReader(encodePostValues(ro.Data)))

	if err != nil {
		return nil, err
	}

	// The content type must be set to a regular form
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return req, nil
}

func encodePostValues(postValues map[string]string) string {
	urlValues := &url.Values{}

	for key, value := range postValues {
		urlValues.Set(key, value)
	}

	return urlValues.Encode() // This will sort all of the string values
}

// proxySettings will default to the default proxy settings if none are provided
// if settings are provided – they will override the environment variables
func (ro RequestOptions) proxySettings(req *http.Request) (*url.URL, error) {
	// No proxies – lets use the default
	if len(ro.Proxies) == 0 {
		return http.ProxyFromEnvironment(req)
	}

	// There was a proxy specified – do we support the protocol?
	if _, ok := ro.Proxies[req.URL.Scheme]; ok {
		return ro.Proxies[req.URL.Scheme], nil
	}

	// Proxies were specified but not for any protocol that we use
	return http.ProxyFromEnvironment(req)

}

// dontUseDefaultClient will tell the "client creator" if a custom client is needed
// it checks the following items (and will create a custom client of these are)
// true
// 1. Do we want to accept invalid SSL certificates?
// 2. Do we want to disable compression?
// 3. Do we want a custom proxy?
// 4. Do we want to change the default timeout for TLS Handshake?
// 5. Do we want to change the default request timeout?
// 6. Do we want to change the default connection timeout?
// 7. Do you want to use the http.Client's cookieJar?
// 8. Do you want to change the request timeout?
// 9. Do you want to set a custom LocalAddr to send the request from
func (ro RequestOptions) dontUseDefaultClient() bool {
	switch {
	case ro.InsecureSkipVerify == true:
	case ro.DisableCompression == true:
	case len(ro.Proxies) != 0:
	case ro.TLSHandshakeTimeout != 0:
	case ro.DialTimeout != 0:
	case ro.DialKeepAlive != 0:
	case len(ro.Cookies) != 0:
	case ro.UseCookieJar != false:
	case ro.RequestTimeout != 0:
	case ro.LocalAddr != nil:
	default:
		return false
	}
	return true
}

// BuildHTTPClient is a function that will return a custom HTTP client based on the request options provided
// the check is in UseDefaultClient
func BuildHTTPClient(ro RequestOptions) *http.Client {

	if ro.HTTPClient != nil {
		return ro.HTTPClient
	}

	// Does the user want to change the defaults?
	if !ro.dontUseDefaultClient() {
		return http.DefaultClient
	}

	// Using the user config for tls timeout or default
	if ro.TLSHandshakeTimeout == 0 {
		ro.TLSHandshakeTimeout = tlsHandshakeTimeout
	}

	// Using the user config for dial timeout or default
	if ro.DialTimeout == 0 {
		ro.DialTimeout = dialTimeout
	}

	// Using the user config for dial keep alive or default
	if ro.DialKeepAlive == 0 {
		ro.DialKeepAlive = dialKeepAlive
	}

	if ro.RequestTimeout == 0 {
		ro.RequestTimeout = requestTimeout
	}

	var cookieJar http.CookieJar

	if ro.UseCookieJar {
		if ro.CookieJar != nil {
			cookieJar = ro.CookieJar
		} else {
			// The function does not return an error ever... so we are just ignoring it
			cookieJar, _ = cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
		}
	}

	return &http.Client{
		Jar:       cookieJar,
		Transport: createHTTPTransport(ro),
		Timeout:   ro.RequestTimeout,
	}
}

func createHTTPTransport(ro RequestOptions) *http.Transport {
	ourHTTPTransport := &http.Transport{
		// These are borrowed from the default transporter
		Proxy: ro.proxySettings,
		Dial: (&net.Dialer{
			Timeout:   ro.DialTimeout,
			KeepAlive: ro.DialKeepAlive,
			LocalAddr: ro.LocalAddr,
		}).Dial,
		TLSHandshakeTimeout: ro.TLSHandshakeTimeout,

		// Here comes the user settings
		TLSClientConfig:    &tls.Config{InsecureSkipVerify: ro.InsecureSkipVerify},
		DisableCompression: ro.DisableCompression,
	}
	EnsureTransporterFinalized(ourHTTPTransport)
	return ourHTTPTransport
}

// buildURLParams returns a URL with all of the params
// Note: This function will override current URL params if they contradict what is provided in the map
// That is what the "magic" is on the last line
func buildURLParams(userURL string, params map[string]string) (string, error) {
	parsedURL, err := url.Parse(userURL)

	if err != nil {
		return "", err
	}

	parsedQuery, err := url.ParseQuery(parsedURL.RawQuery)

	if err != nil {
		return "", nil
	}

	for key, value := range params {
		parsedQuery.Set(key, value)
	}

	return addQueryParams(parsedURL, parsedQuery), nil
}

// addHTTPHeaders adds any additional HTTP headers that need to be added are added here including:
// 1. Custom User agent
// 2. Authorization Headers
// 3. Any other header requested
func addHTTPHeaders(ro *RequestOptions, req *http.Request) {
	for key, value := range ro.Headers {
		req.Header.Set(key, value)
	}

	if ro.UserAgent != "" {
		req.Header.Set("User-Agent", ro.UserAgent)
	} else {
		req.Header.Set("User-Agent", localUserAgent)
	}

	if ro.Host != "" {
		req.Host = ro.Host
	}

	if ro.Auth != nil {
		req.SetBasicAuth(ro.Auth[0], ro.Auth[1])
	}

	if ro.IsAjax == true {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
}

func addCookies(ro *RequestOptions, req *http.Request) {
	for _, c := range ro.Cookies {
		req.AddCookie(c)
	}
}

func addQueryParams(parsedURL *url.URL, parsedQuery url.Values) string {
	return strings.Join([]string{strings.Replace(parsedURL.String(), "?"+parsedURL.RawQuery, "", -1), parsedQuery.Encode()}, "?")
}

func buildURLStruct(userURL string, URLStruct interface{}) (string, error) {
	parsedURL, err := url.Parse(userURL)

	if err != nil {
		return "", err
	}

	parsedQuery, err := url.ParseQuery(parsedURL.RawQuery)

	if err != nil {
		return "", err
	}

	queryStruct, err := query.Values(URLStruct)
	if err != nil {
		return "", err
	}

	for key, value := range queryStruct {
		for _, v := range value {
			parsedQuery.Add(key, v)
		}
	}

	return addQueryParams(parsedURL, parsedQuery), nil
}
