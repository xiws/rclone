package wocloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	wapi "github.com/rclone/rclone/backend/wocloud/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/obscure"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestAES(t *testing.T) {
	const plain = `{"hello":"世界","n":1}`
	// Generated independently with
	// printf '%s' "$plain" | openssl enc -aes-128-cbc -K <key> -iv <iv> -base64 -A
	const want = "K7Hm+HRR9viOtf9HIwGNrjbhxxKlz3PEaREmj0sFMs0="
	got, err := aesEncrypt(plain, defaultClientSecret)
	if err != nil {
		t.Fatalf("aesEncrypt: %v", err)
	}
	if got != want {
		t.Fatalf("aesEncrypt = %q, want %q", got, want)
	}
	decoded, err := aesDecrypt(got, defaultClientSecret)
	if err != nil {
		t.Fatalf("aesDecrypt: %v", err)
	}
	if string(decoded) != plain {
		t.Fatalf("aesDecrypt = %q, want %q", decoded, plain)
	}
	if _, err := aesEncrypt(plain, "short"); err == nil {
		t.Fatal("expected error for short key")
	}
	if _, err := aesDecrypt("not base64!!", defaultClientSecret); err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestMakeSign(t *testing.T) {
	// md5("QueryAllFiles" + "1789867404906" + "136334" + "wohome" + "")
	const want = "0de63a13ff80ac31dfa17044f80f8c93"
	got := makeSign("QueryAllFiles", 1789867404906, 136334, channelWoHome, "")
	if got != want {
		t.Fatalf("makeSign = %q, want %q", got, want)
	}
}

func TestParseList(t *testing.T) {
	const data = `{
		"files": [
			{"name":"dir","id":"id-dir","type":0,"size":0,"fid":"","fileType":"","modifyTime":"20261025095616"},
			{"name":"a.txt","id":"id-file","fid":"fid-file","type":1,"size":"12","fileType":4,"fileVersion":"2","parentDirectoryId":"id-dir","modifyTime":"20261025110250"}
		],
		"systemDirs": []
	}`
	var result wapi.ListResponse
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Files) != 2 {
		t.Fatalf("got %d files", len(result.Files))
	}
	if result.Files[0].Type != wapi.TypeDirectory || result.Files[0].ID != "id-dir" {
		t.Fatalf("bad directory: %+v", result.Files[0])
	}
	file := result.Files[1]
	if file.Type != wapi.TypeFile || file.Size != 12 || file.FileVersion != 2 {
		t.Fatalf("bad file: %+v", file)
	}
	if string(file.FileType) != "4" {
		t.Fatalf("bad fileType: %q", file.FileType)
	}
}

func TestParseTime(t *testing.T) {
	got := parseTime("20261025110250")
	want := time.Date(2026, 10, 25, 11, 2, 50, 0, chinaTime)
	if !got.Equal(want) {
		t.Fatalf("parseTime = %v, want %v", got, want)
	}
	if !parseTime("bad").IsZero() {
		t.Fatal("expected zero time for invalid input")
	}
}

func TestFileTypeOf(t *testing.T) {
	for name, want := range map[string]string{
		"a.jpg":     "1",
		"a.mp4":     "2",
		"a.mp3":     "3",
		"a.pdf":     "4",
		"a.bin":     "5",
		"noext":     "5",
		"a.JPG":     "1",
		"a.txt":     "4",
		"中文 文件.txt": "4",
	} {
		if got := fileTypeOf(name); got != want {
			t.Errorf("fileTypeOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDecodeData(t *testing.T) {
	ctx := context.Background()
	d := newDispatcher(ctx, "http://127.0.0.1:1", defaultClientID, defaultClientSecret, testToken)

	encoded, err := aesEncrypt(`{"files":[]}`, testToken)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(strconv.Quote(encoded))
	got, err := d.decodeData(channelWoHome, raw)
	if err != nil {
		t.Fatalf("decodeData: %v", err)
	}
	if string(got) != `{"files":[]}` {
		t.Fatalf("decodeData = %q", got)
	}

	// api-user responses are encrypted with the client secret.
	encoded, err = aesEncrypt(`{"userId":"u"}`, defaultClientSecret)
	if err != nil {
		t.Fatal(err)
	}
	got, err = d.decodeData(channelAPIUser, json.RawMessage(strconv.Quote(encoded)))
	if err != nil {
		t.Fatalf("decodeData api-user: %v", err)
	}
	if string(got) != `{"userId":"u"}` {
		t.Fatalf("decodeData api-user = %q", got)
	}

	// A plain JSON string should be accepted as-is for api-user.
	plain := `{"plain":true}`
	got, err = d.decodeData(channelAPIUser, json.RawMessage(strconv.Quote(plain)))
	if err != nil {
		t.Fatalf("decodeData plain: %v", err)
	}
	if string(got) != plain {
		t.Fatalf("decodeData plain = %q", got)
	}
}

func TestDispatch(t *testing.T) {
	ctx := context.Background()
	var handlerErr error

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api-user/dispatcher" {
			var req wapi.Request
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				handlerErr = err
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if !req.Body.Secret || req.Body.ClientID != defaultClientID {
				handlerErr = fmt.Errorf("bad api-user body: %+v", req.Body)
			}
			params, err := aesDecrypt(req.Body.Param, defaultClientSecret)
			if err != nil {
				handlerErr = err
			}
			var decoded map[string]any
			if err := json.Unmarshal(params, &decoded); err != nil {
				handlerErr = err
			}
			if decoded["accessToken"] != "token" {
				handlerErr = fmt.Errorf("bad api-user params: %v", decoded)
			}
			enc, _ := aesEncrypt(`{"userId":"u"}`, defaultClientSecret)
			_ = json.NewEncoder(w).Encode(wapi.Response{
				Status: "200",
				RSP:    wapi.RSP{Code: "0000", Desc: "ok", Data: json.RawMessage(strconv.Quote(enc))},
			})
			return
		}
		if r.URL.Path != "/wohome/dispatcher" {
			handlerErr = fmt.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("accesstoken"); got != testToken {
			handlerErr = fmt.Errorf("bad accesstoken header %q", got)
		}
		var req wapi.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			handlerErr = err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Header.Key != "QueryAllFiles" || req.Header.Channel != channelWoHome || req.Header.Version != "" {
			handlerErr = fmt.Errorf("bad header: %+v", req.Header)
		}
		if want := makeSign(req.Header.Key, req.Header.ResTime, req.Header.ReqSeq, req.Header.Channel, ""); req.Header.Sign != want {
			handlerErr = fmt.Errorf("bad sign %q want %q", req.Header.Sign, want)
		}
		if !req.Body.Key || req.Body.Secret || req.Body.ClientID != "" {
			handlerErr = fmt.Errorf("bad wohome body: %+v", req.Body)
		}
		params, err := aesDecrypt(req.Body.Param, testToken)
		if err != nil {
			handlerErr = err
		}
		var decoded map[string]any
		if err := json.Unmarshal(params, &decoded); err != nil {
			handlerErr = err
		}
		if decoded["clientId"] != defaultClientID {
			handlerErr = fmt.Errorf("clientId not injected: %v", decoded)
		}
		if decoded["spaceType"] != "0" {
			handlerErr = fmt.Errorf("bad spaceType: %v", decoded)
		}
		enc, _ := aesEncrypt(`{"files":[{"name":"x","type":1,"size":3}]}`, testToken)
		_ = json.NewEncoder(w).Encode(wapi.Response{
			Status: "200",
			RSP:    wapi.RSP{Code: "0000", Desc: "ok", Data: json.RawMessage(strconv.Quote(enc))},
		})
	}))
	defer srv.Close()

	d := newDispatcher(ctx, srv.URL, defaultClientID, defaultClientSecret, testToken)
	raw, err := d.call(ctx, channelWoHome, "QueryAllFiles", &wapi.ListParams{
		SpaceParams:       wapi.SpaceParams{SpaceType: "0"},
		ParentDirectoryID: "0",
		PageNum:           0,
		PageSize:          10,
		SortRule:          "1",
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var result wapi.ListResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].Name != "x" {
		t.Fatalf("bad result: %+v", result)
	}

	raw, err = d.call(ctx, channelAPIUser, "AppQueryUser", &wapi.QueryUserParams{AccessToken: "token"})
	if err != nil {
		t.Fatalf("api-user call: %v", err)
	}
	if string(raw) != `{"userId":"u"}` {
		t.Fatalf("bad api-user result: %s", raw)
	}
	if handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
}

func TestDispatchAuthError(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(wapi.Response{
			Status: "200",
			RSP:    wapi.RSP{Code: "1001", Desc: "无效登录信息"},
		})
	}))
	defer srv.Close()
	d := newDispatcher(ctx, srv.URL, defaultClientID, defaultClientSecret, testToken)
	_, err := d.call(ctx, channelWoHome, "QueryAllFiles", &wapi.ListParams{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !isAuthError(err) {
		t.Fatalf("isAuthError(%v) = false", err)
	}
	if !isAPIError(err, "1001") {
		t.Fatalf("isAPIError(%v) = false", err)
	}
}

func TestUpload(t *testing.T) {
	ctx := context.Background()
	var handlerErr error
	var gotFile []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != uploadPath {
			handlerErr = fmt.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			handlerErr = err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		form := r.MultipartForm
		for _, key := range []string{"uniqueId", "accessToken", "fileName", "psToken", "fileSize", "totalPart", "partSize", "partIndex", "channel", "directoryId", "fileInfo"} {
			if _, ok := form.Value[key]; !ok {
				handlerErr = fmt.Errorf("missing field %q", key)
			}
		}
		if got := r.FormValue("channel"); got != "wocloud" {
			handlerErr = fmt.Errorf("bad channel %q", got)
		}
		if got := r.FormValue("fileName"); got != "a.txt" {
			handlerErr = fmt.Errorf("bad fileName %q", got)
		}
		if got := r.FormValue("directoryId"); got != "dir-id" {
			handlerErr = fmt.Errorf("bad directoryId %q", got)
		}
		if got := r.FormValue("fileSize"); got != "5" {
			handlerErr = fmt.Errorf("bad fileSize %q", got)
		}
		if !regexp.MustCompile(`^\d+_[0-9a-f]{6}$`).MatchString(r.FormValue("uniqueId")) {
			handlerErr = fmt.Errorf("bad uniqueId %q", r.FormValue("uniqueId"))
		}
		dec, err := aesDecrypt(r.FormValue("fileInfo"), testToken)
		if err != nil {
			handlerErr = err
		}
		var info wapi.UploadFileInfo
		if err := json.Unmarshal(dec, &info); err != nil {
			handlerErr = err
		}
		if len(info.BatchNo) != 32 {
			handlerErr = fmt.Errorf("bad batchNo %q", info.BatchNo)
		}
		if info.SpaceType != "0" || info.DirectoryID != "dir-id" || info.FileName != "a.txt" || info.FileSize != 5 || info.FileType != "4" {
			handlerErr = fmt.Errorf("bad fileInfo: %+v", info)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			handlerErr = err
		} else {
			gotFile, _ = io.ReadAll(file)
			_ = file.Close()
		}
		_ = json.NewEncoder(w).Encode(wapi.UploadResponse{
			Code: "0000",
			Msg:  "上传成功",
			Data: wapi.UploadData{Fid: "fid", WcFileID: "id", FileVersion: "1"},
		})
	}))
	defer srv.Close()

	f := &Fs{
		opt: Options{
			AccessToken:  testToken,
			SpaceType:    "0",
			ClientID:     defaultClientID,
			ClientSecret: defaultClientSecret,
			UploadHost:   srv.URL,
			PageSize:     defaultPageSize,
			Enc:          defaultEncoding,
		},
	}
	f.disp = newDispatcher(ctx, "http://127.0.0.1:1", defaultClientID, defaultClientSecret, testToken)
	data, err := f.upload(ctx, strings.NewReader("hello"), 5, "a.txt", "dir-id")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if data.WcFileID != "id" || data.Fid != "fid" {
		t.Fatalf("bad upload data: %+v", data)
	}
	if handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
	if string(gotFile) != "hello" {
		t.Fatalf("file content = %q", gotFile)
	}
}

func TestUploadError(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"130004","msg":"文件夹不存在"}`))
	}))
	defer srv.Close()

	f := &Fs{
		opt: Options{
			AccessToken:  testToken,
			SpaceType:    "0",
			ClientID:     defaultClientID,
			ClientSecret: defaultClientSecret,
			UploadHost:   srv.URL,
			PageSize:     defaultPageSize,
			Enc:          defaultEncoding,
		},
	}
	f.disp = newDispatcher(ctx, "http://127.0.0.1:1", defaultClientID, defaultClientSecret, testToken)
	_, err := f.upload(ctx, strings.NewReader("hello"), 5, "a.txt", "dir-id")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !isAPIError(err, "130004") {
		t.Fatalf("expected 130004 error, got %v", err)
	}
}

func TestSpoolToTemp(t *testing.T) {
	in := strings.NewReader(strings.Repeat("x", 12345))
	out, size, cleanup, err := spoolToTemp(in)
	if err != nil {
		t.Fatalf("spoolToTemp: %v", err)
	}
	defer cleanup()
	if size != 12345 {
		t.Fatalf("size = %d", size)
	}
	read, err := io.ReadAll(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read) != 12345 {
		t.Fatalf("read %d bytes", len(read))
	}
}

func TestConfigLogin(t *testing.T) {
	ctx := context.Background()
	var handlerErr error

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api-user/dispatcher" {
			handlerErr = fmt.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var req wapi.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			handlerErr = err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		params, err := aesDecrypt(req.Body.Param, defaultClientSecret)
		if err != nil {
			handlerErr = err
		}
		var decoded map[string]any
		if err := json.Unmarshal(params, &decoded); err != nil {
			handlerErr = err
		}
		switch req.Header.Key {
		case "PcWebLogin":
			if decoded["verifyCode"] == "" {
				_ = json.NewEncoder(w).Encode(wapi.Response{
					Status: "200",
					RSP:    wapi.RSP{Code: "6006", Desc: "需要图形验证码"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(wapi.Response{
				Status: "200",
				RSP:    wapi.RSP{Code: "0000", Desc: "ok", Data: json.RawMessage(`{"needSmsCode":"1"}`)},
			})
		case "PcLoginVerifyCode":
			_ = json.NewEncoder(w).Encode(wapi.Response{
				Status: "200",
				RSP:    wapi.RSP{Code: "0000", Desc: "ok", Data: json.RawMessage(`{"access_token":"tok123"}`)},
			})
		default:
			handlerErr = fmt.Errorf("unexpected operation %q", req.Header.Key)
		}
	}))
	defer srv.Close()

	m := configmap.Simple{
		"api_host":      srv.URL,
		"client_id":     defaultClientID,
		"client_secret": defaultClientSecret,
		"space_type":    "0",
		"access_token":  "",
	}

	out, err := Config(ctx, "test", m, fs.ConfigIn{State: ""})
	if err != nil {
		t.Fatalf("Config(): %v", err)
	}
	if out == nil || out.State != "phone" {
		t.Fatalf("expected goto phone, got %+v", out)
	}

	out, err = Config(ctx, "test", m, fs.ConfigIn{State: "phone"})
	if err != nil {
		t.Fatalf("Config phone: %v", err)
	}
	if out == nil || out.State != "password" || out.Option == nil {
		t.Fatalf("expected phone prompt, got %+v", out)
	}

	out, err = Config(ctx, "test", m, fs.ConfigIn{State: "password", Result: "13800000000"})
	if err != nil {
		t.Fatalf("Config phone: %v", err)
	}
	if out == nil || out.State != "login" || out.Option == nil || !out.Option.IsPassword {
		t.Fatalf("expected password prompt, got %+v", out)
	}

	out, err = Config(ctx, "test", m, fs.ConfigIn{State: "login", Result: obscure.MustObscure("pw")})
	if err != nil {
		t.Fatalf("Config login: %v", err)
	}
	if out == nil || out.State != "captcha" || out.Option == nil {
		t.Fatalf("expected captcha prompt, got %+v", out)
	}
	if !strings.Contains(out.Option.Help, "/api-user/getverifycode?uuid=") {
		t.Fatalf("captcha help missing URL: %q", out.Option.Help)
	}

	out, err = Config(ctx, "test", m, fs.ConfigIn{State: "captcha", Result: "abcd"})
	if err != nil {
		t.Fatalf("Config captcha: %v", err)
	}
	if out == nil || out.State != "sms" || out.Option == nil {
		t.Fatalf("expected sms prompt, got %+v", out)
	}

	out, err = Config(ctx, "test", m, fs.ConfigIn{State: "sms", Result: "123456"})
	if err != nil {
		t.Fatalf("Config sms: %v", err)
	}
	if out != nil {
		t.Fatalf("expected finished, got %+v", out)
	}
	if got, _ := m.Get("access_token"); got != "tok123" {
		t.Fatalf("access_token = %q", got)
	}
	if handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
}

func TestConfigExistingToken(t *testing.T) {
	ctx := context.Background()
	m := configmap.Simple{
		"api_host":     "http://127.0.0.1:1",
		"access_token": "existing",
	}
	out, err := Config(ctx, "test", m, fs.ConfigIn{State: ""})
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if out == nil || out.State != "refresh" || out.Option == nil {
		t.Fatalf("expected refresh prompt, got %+v", out)
	}
	out, err = Config(ctx, "test", m, fs.ConfigIn{State: "refresh", Result: "false"})
	if err != nil {
		t.Fatalf("Config refresh: %v", err)
	}
	if out != nil {
		t.Fatalf("expected finished, got %+v", out)
	}
}

func TestConfigLostState(t *testing.T) {
	ctx := context.Background()
	m := configmap.Simple{"api_host": "http://127.0.0.1:1"}
	_, err := Config(ctx, "test", m, fs.ConfigIn{State: "sms", Result: "123456"})
	if err == nil || !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("expected a reconnect error, got %v", err)
	}
}
