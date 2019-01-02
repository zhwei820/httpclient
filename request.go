// Copyright 2018 ouqiang authors
//
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package httpclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout = 20 * time.Second
)

var (
	// 如果设置了Accept-Encoding, 不会自动解压
	defaultHeader = map[string]string{
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8,ja;q=0.7",
		"Cache-Control":   "no-cache",
		"Pragma":          "no-cache",
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_4) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/66.0.3359.170 Safari/537.36",
	}
)

type options struct {
	client              *http.Client
	debug               bool
	timeout             time.Duration
	proxyURL            string
	retryTimes          int
	enableDefaultHeader bool
	disableKeepAlive    bool
	shouldRetryFunc     func(*http.Response, error) bool
}

type Option func(*options)

// WithClient 自定义http client
func WithClient(client *http.Client) Option {
	return func(opt *options) {
		opt.client = client
	}
}

// WithShouldRetryFunc 自定义是否需要重试
func WithShouldRetryFunc(f func(*http.Response, error) bool) Option {
	return func(opt *options) {
		opt.shouldRetryFunc = f
	}
}

// WithEnableDefaultHeader 设置默认header
func WithEnableDefaultHeader() Option {
	return func(opt *options) {
		opt.enableDefaultHeader = true
	}
}

// WithRetryTime 设置重试次数
func WithRetryTime(retryTimes int) Option {
	return func(opt *options) {
		opt.retryTimes = retryTimes
	}
}

// WithProxyURL 设置代理
func WithProxyURL(proxyURL string) Option {
	return func(opt *options) {
		opt.proxyURL = proxyURL
	}
}

// WithTimeout 设置超时
func WithTimeout(timeout time.Duration) Option {
	return func(opt *options) {
		opt.timeout = timeout
	}
}

// WithDisableKeepAlive 连接重用
func WithDisableKeepAlive() Option {
	return func(opt *options) {
		opt.disableKeepAlive = true
	}
}

// WithDebug 开启调试模式
func WithDebug() Option {
	return func(opt *options) {
		opt.debug = true
	}
}

// Request http请求
type Request struct {
	opts options
}

// NewRequest 创建request
func NewRequest(opt ...Option) *Request {
	req := &Request{}
	req.opts = options{}
	for _, o := range opt {
		o(&req.opts)
	}

	trans := &http.Transport{
		Proxy: func(request *http.Request) (*url.URL, error) {
			if req.opts.proxyURL != "" {
				return url.Parse(req.opts.proxyURL)
			}

			return nil, nil
		},
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if req.opts.disableKeepAlive {
		trans.DisableKeepAlives = true
	}

	if req.opts.client == nil {
		req.opts.client = &http.Client{}
	}
	if req.opts.client.Transport == nil {
		req.opts.client.Transport = trans
	}
	if req.opts.timeout == 0 {
		req.opts.client.Timeout = defaultTimeout
	}
	if req.opts.shouldRetryFunc == nil {
		req.opts.shouldRetryFunc = req.shouldRetry
	}

	return req
}

// Get get请求
func (req *Request) Get(url string, data url.Values, header http.Header) (*Response, error) {
	url = req.makeURLWithParams(url, data)

	return req.do(http.MethodGet, url, nil, header)
}

// Post 普通post请求
func (req *Request) Post(url string, data interface{}, header http.Header) (*Response, error) {
	if header == nil {
		header = make(http.Header)
	}
	header.Set("Content-Type", "application/x-www-form-urlencoded")

	return req.do(http.MethodPost, url, data, header)
}

// PostJSON 发送json body
func (req *Request) PostJSON(url string, data interface{}, header http.Header) (*Response, error) {
	if header == nil {
		header = make(http.Header)
	}
	header.Set("Content-Type", "application/json")
	var body interface{}
	switch data.(type) {
	case string, []byte, io.Reader:
		body = data
	default:
		var err error
		body, err = json.Marshal(data)
		if err != nil {
			return nil, err
		}
	}

	return req.do(http.MethodPost, url, body, header)
}

func (req *Request) do(method string, url string, data interface{}, header http.Header) (*Response, error) {
	targetReq, err := req.build(method, url, data, header)
	if err != nil {
		return nil, err
	}
	req.beforeRequest(targetReq)
	execTimes := 1
	if req.opts.retryTimes > 0 {
		execTimes += req.opts.retryTimes
	}
	var resp *http.Response
	for i := 0; i < execTimes; i++ {
		resp, err = req.opts.client.Do(targetReq)
		req.afterResponse(resp, err)
		if req.opts.retryTimes > 0 && !req.opts.shouldRetryFunc(resp, err) {
			break
		}
	}
	if err != nil {
		return nil, err
	}

	return newResponse(resp), err
}

// 构造http.Request
func (req *Request) build(method string, url string, data interface{}, header http.Header) (*http.Request, error) {
	body := req.makeBody(data)
	targetReq, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if header == nil {
		header = make(http.Header)
	}
	targetReq.Header = header
	host := header.Get("Host")
	if host != "" {
		targetReq.Host = host
	}
	if req.opts.enableDefaultHeader {
		for k, v := range defaultHeader {
			targetReq.Header.Add(k, v)
		}
	}

	return targetReq, nil
}

func (req *Request) beforeRequest(r *http.Request) {
	req.dumpRequestIfNeed(r)
}

func (req *Request) afterResponse(resp *http.Response, err error) {
	req.dumpResponseIfNeed(resp, err)
}

// request调试输出
func (req *Request) dumpRequestIfNeed(r *http.Request) {
	if !req.opts.debug {
		return
	}
	reqDump, err := httputil.DumpRequestOut(r, true)
	if err != nil {
		panic(err)
	}
	fmt.Printf("[Request]\n\n%s\n", reqDump)
}

// response调试输出
func (req *Request) dumpResponseIfNeed(resp *http.Response, err error) {
	if !req.opts.debug {
		return
	}
	if err != nil {
		fmt.Printf("[Response]\n\n%s\n", err)
		return
	}
	respDump, err := httputil.DumpResponse(resp, true)
	if err != nil {
		panic(err)
	}
	fmt.Printf("[Response]\n\n %s\n", respDump)
}

// 是否要重试
func (req *Request) shouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	if resp == nil {
		return true
	}
	if resp.StatusCode != http.StatusOK {
		return true
	}

	return false
}

// 参数追加到url末尾
func (req *Request) makeURLWithParams(url string, data url.Values) string {
	if url == "" {
		return url
	}
	if data == nil {
		return url
	}
	params := data.Encode()
	if strings.Contains(url, "?") {
		if url[len(url)-1] != '?' {
			url += "&"
		}
	} else {
		url += "?"
	}
	url += params

	return url
}

// 生成请求Body
func (req *Request) makeBody(data interface{}) io.Reader {
	if data == nil {
		return nil
	}
	var body io.Reader
	switch v := data.(type) {
	case string:
		body = strings.NewReader(v)
	case []byte:
		body = bytes.NewBuffer(v)
	case url.Values:
		body = strings.NewReader(v.Encode())
	case io.Reader:
		body = v
	default:
		panic("data is not support type")
	}

	return body
}
