package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	logger = log.New(io.Discard, "", 0)
	os.Exit(m.Run())
}

func useTestHTTPClient(t *testing.T, pageSize uint) *dataProvider {
	t.Helper()
	previous := httpclient
	flags := Flags{httpFlags: httpFlags{
		PageSize: pageSize,
		Timeout:  2 * time.Second,
		Headers:  map[string]string{"Authorization": "test-token-1"},
	}}
	httpclient = &HTTPClient{cfg: flags.httpFlags}
	t.Cleanup(func() { httpclient = previous })
	return newDataProvider(flags)
}

func TestCMDBPaginationPreservesPathAndFixedQuery(t *testing.T) {
	var requests []url.Values
	var requestMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "//api/hosts" {
			t.Errorf("path = %q, want //api/hosts", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "test-token-1" {
			t.Errorf("Authorization = %q", got)
		}
		query := r.URL.Query()
		requestMu.Lock()
		requests = append(requests, query)
		requestMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch query.Get("pageNum") {
		case "1", "2":
			fmt.Fprintf(w, `{"list":[{"appid":"app","hostname":"vm-%s"}]}`, query.Get("pageNum"))
		default:
			fmt.Fprint(w, `{"list":[]}`)
		}
	}))
	defer server.Close()
	dp := useTestHTTPClient(t, 5)
	items, err := dp.getCMDBRawData(server.URL + "//api/hosts?cluster=7&pageSize=99&pageNum=99")
	if err != nil {
		t.Fatal(err)
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	if len(items) != 2 || len(requests) != 3 {
		t.Fatalf("items=%d requests=%d, want 2 items and 3 requests", len(items), len(requests))
	}
	for i, query := range requests {
		want := url.Values{"cluster": {"7"}, "pageSize": {"5"}, "pageNum": {fmt.Sprint(i + 1)}}
		if !reflect.DeepEqual(query, want) {
			t.Errorf("page %d query=%v, want %v", i+1, query, want)
		}
	}
}

func TestCMDBRejectsMalformedList(t *testing.T) {
	for _, body := range []string{`{"error":"denied"}`, `{"list":null}`, `{"list":{}}`, `{"list":[1]}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			if _, err := useTestHTTPClient(t, 5).getCMDBRawData(server.URL); err == nil {
				t.Fatal("malformed list must fail")
			}
		})
	}
}

func TestCMDBRejectsRepeatedPage(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `{"list":[{"appid":"app","hostname":"vm"}]}`)
	}))
	defer server.Close()
	_, err := useTestHTTPClient(t, 5).getCMDBRawData(server.URL)
	if err == nil || !strings.Contains(err.Error(), "内容重复") || requests.Load() != 2 {
		t.Fatalf("err=%v requests=%d, want repeated-page error after 2 requests", err, requests.Load())
	}
}

func TestCMDBPageFailureReturnsNoPartialResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageNum") == "2" {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"unavailable"}`)
			return
		}
		fmt.Fprint(w, `{"list":[{"appid":"app","hostname":"vm"}]}`)
	}))
	defer server.Close()
	items, err := useTestHTTPClient(t, 5).getCMDBRawData(server.URL)
	if err == nil || len(items) != 0 {
		t.Fatalf("items=%v err=%v, want failure without partial data", items, err)
	}
}

func TestCMDBRejectsZeroPageSize(t *testing.T) {
	if _, err := newDataProvider(Flags{}).getCMDBRawData("http://unused.invalid"); err == nil {
		t.Fatal("zero page size must fail before HTTP request")
	}
}

func TestHTTPClientDecodesExplicitGzipResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Error("expected caller's explicit Accept-Encoding header")
		}
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		if _, err := writer.Write([]byte(`{"list":[{"appid":"app"}]}`)); err != nil {
			t.Error(err)
		}
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	useTestHTTPClient(t, 5)
	httpclient.cfg.Headers["Accept-Encoding"] = "gzip"
	response, err := httpclient.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Get("list").GetIndex(0).Get("appid").MustString(); got != "app" {
		t.Fatalf("appid=%q, want app", got)
	}
}

func TestBizServiceIndexSeparatesTypes(t *testing.T) {
	dp := newDataProvider(Flags{})
	items := []BizService{
		{Base: Base{ID: 1}, Name: "same-app", Type: BIZ_SERVICE_TYPE_CHOST},
		{Base: Base{ID: 2}, Name: "same-app", Type: BIZ_SERVICE_TYPE_POD_GROUP},
	}
	if err := dp.indexBizServices(items); err != nil {
		t.Fatal(err)
	}
	if len(dp.oldBizServiceKeyToItem) != 2 {
		t.Fatalf("index contains %d services, want 2", len(dp.oldBizServiceKeyToItem))
	}
	if got := dp.oldBizServiceKeyToItem[bizServiceKey{BIZ_SERVICE_TYPE_POD_GROUP, "same-app"}].ID; got != 2 {
		t.Fatalf("pod group service ID=%d, want 2", got)
	}
}

func TestBizServiceIndexRejectsAmbiguousDuplicates(t *testing.T) {
	dp := newDataProvider(Flags{})
	items := []BizService{
		{Base: Base{ID: 1}, Name: "same-app", Type: BIZ_SERVICE_TYPE_CHOST},
		{Base: Base{ID: 2}, Name: "same-app", Type: BIZ_SERVICE_TYPE_CHOST},
	}
	if err := dp.indexBizServices(items); err == nil {
		t.Fatal("same type/name duplicates must fail")
	}
}
