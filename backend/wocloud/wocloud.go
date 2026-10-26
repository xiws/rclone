// Package wocloud provides an interface to the China Unicom WoCloud
// (联通云盘) cloud storage service.
//
// The service has no per-operation URLs. All operations are POSTed to a
// single dispatcher endpoint with the operation name in a signed header
// and the parameters JSON encrypted with AES-128-CBC. Uploads and
// downloads use separate hosts returned by the dispatcher.
package wocloud

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	wapi "github.com/rclone/rclone/backend/wocloud/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/dircache"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
	"golang.org/x/time/rate"
)

const (
	defaultAPIHost      = "https://panservice.mail.wo.cn"
	defaultUploadHost   = "https://hyupload.pan.wo.cn"
	defaultClientID     = "1001000021"
	defaultClientSecret = "XFmi9GS2hzk98jGX"
	aesIV               = "wNSOYIB1k1DjY5lA"
	rootID              = "0"
	defaultPageSize     = 100
	maxPageSize         = 1000
	minSleep            = 10 * time.Millisecond
	maxSleep            = 10 * time.Second
	decayConstant       = 1
	uploadPath          = "/openapi/client/upload2C"
	maxDeleteBatch      = 100
	maxPages            = 100000
	channelAPIUser      = "api-user"
	channelWoHome       = "wohome"
	sortRule            = "1"
)

// defaultEncoding is the filename encoding used unless overridden.
//
// The server rejects * ? < > with HTTP 500, so those must be encoded. The
// other characters are legal on this service but are encoded so that
// remotes are usable from Windows.
const defaultEncoding = encoder.Display |
	encoder.EncodeWin |
	encoder.EncodeBackSlash |
	encoder.EncodeLeftSpace |
	encoder.EncodeRightSpace |
	encoder.EncodeLeftPeriod |
	encoder.EncodeRightPeriod |
	encoder.EncodeInvalidUtf8

// chinaTime is the time zone used by the modifyTime/createTime fields.
// They are formatted YYYYMMDDHHMMSS in China Standard Time.
var chinaTime = time.FixedZone("CST", 8*60*60)

// Register with Fs
func init() {
	fs.Register(&fs.RegInfo{
		Name:        "wocloud",
		Description: "China Unicom WoCloud",
		NewFs:       NewFs,
		Config:      Config,
		Options: []fs.Option{{
			Name: "access_token",
			Help: `Access token used for the WoCloud API.

Leave this blank to log in with a phone number and SMS code when rclone
configures the remote. You can also paste a token obtained from the
official web client here.

The token is sensitive: it grants full access to the account.`,
			Sensitive: true,
		}, {
			Name: "phone",
			Help: `Phone number used to log in.

This is only used by the interactive login and is not required when an
access_token is supplied.`,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name:     "space_type",
			Help:     "Cloud space to use: 0 personal, 1 family, 4 private.",
			Default:  "0",
			Advanced: true,
			Examples: []fs.OptionExample{{
				Value: "0",
				Help:  "Personal cloud",
			}, {
				Value: "1",
				Help:  "Family cloud (requires family_id)",
			}, {
				Value: "4",
				Help:  "Private space (requires ps_token)",
			}},
			Exclusive: true,
		}, {
			Name: "family_id",
			Help: `Family ID for the family cloud.

Only used when space_type is 1.`,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name: "ps_token",
			Help: `Private space token.

Only used when space_type is 4. Obtaining this token is outside the
scope of this backend.`,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name: "root_folder_id",
			Help: `ID of the root folder.

Leave this as "0" (the cloud disk root) unless you want to restrict
rclone to a particular directory.`,
			Default:  rootID,
			Advanced: true,
		}, {
			Name: "api_host",
			Help: `Dispatcher host.

Normally this should be left alone. It is configurable so that the
backend can be tested against a local server.`,
			Default:  defaultAPIHost,
			Advanced: true,
		}, {
			Name: "upload_host",
			Help: `Upload host.

Leave blank to discover it with the GetZoneInfo operation. If discovery
fails the backend falls back to the known production host.`,
			Default:  "",
			Advanced: true,
		}, {
			Name: "client_id",
			Help: `Client ID used to sign dispatcher requests.

This is not user specific. It is configurable in case the service
changes the value.`,
			Default:   defaultClientID,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name: "client_secret",
			Help: `Client secret used to encrypt dispatcher requests.

This is not user specific. It is configurable in case the service
changes the value.`,
			Default:   defaultClientSecret,
			Advanced:  true,
			Sensitive: true,
		}, {
			Name:     "page_size",
			Help:     "Number of entries to request in each listing call.",
			Default:  defaultPageSize,
			Advanced: true,
		}, {
			Name: "poll_timeout",
			Help: `How long to wait for the listing to reflect a change.

WoCloud updates its listings asynchronously after deletes and moves.
rclone waits up to this long for the change to become visible so that
the next listing is consistent.

Set to 0 to disable waiting.`,
			Default:  fs.Duration(30 * time.Second),
			Advanced: true,
		}, {
			Name:     config.ConfigEncoding,
			Help:     config.ConfigEncodingHelp,
			Advanced: true,
			Default:  defaultEncoding,
		}},
	})
}

// Options defines the configuration for this backend
type Options struct {
	AccessToken  string               `config:"access_token"`
	Phone        string               `config:"phone"`
	SpaceType    string               `config:"space_type"`
	FamilyID     string               `config:"family_id"`
	PsToken      string               `config:"ps_token"`
	RootFolderID string               `config:"root_folder_id"`
	APIHost      string               `config:"api_host"`
	UploadHost   string               `config:"upload_host"`
	ClientID     string               `config:"client_id"`
	ClientSecret string               `config:"client_secret"`
	PageSize     int                  `config:"page_size"`
	PollTimeout  fs.Duration          `config:"poll_timeout"`
	Enc          encoder.MultiEncoder `config:"encoding"`
}

// dispatcher talks to the WoCloud dispatcher endpoint.
type dispatcher struct {
	srv          *rest.Client
	pacer        *fs.Pacer
	clientID     string
	clientSecret string
	token        string
}

// Fs represents a remote WoCloud account
type Fs struct {
	name       string             // name of this remote
	root       string             // the path we are working on
	opt        Options            // parsed options
	features   *fs.Features       // optional features
	disp       *dispatcher        // dispatcher connection
	dirCache   *dircache.DirCache // map of directory path to directory ID
	uploadMu   sync.Mutex         // protects uploadHost and uploadSrv
	uploadSrv  *rest.Client       // lazy client for the upload host
	uploadHost string             // lazy upload host
}

// Object describes a WoCloud object
type Object struct {
	fs          *Fs       // what this object is part of
	remote      string    // the remote path
	size        int64     // size of the object
	modTime     time.Time // modification time of the object
	id          string    // file ID used for rename/delete
	fid         string    // file ID used for downloads
	dirID       string    // ID of the parent directory
	fileType    string    // document/image/video category
	fileVersion int64     // server side file version
	md5         string    // cached MD5 from the ETag
}

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string {
	return f.name
}

// Root of the remote (as passed into NewFs)
func (f *Fs) Root() string {
	return f.root
}

// String converts this Fs to a string
func (f *Fs) String() string {
	return fmt.Sprintf("wocloud root %q", f.root)
}

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features {
	return f.features
}

// Precision returns the precision of the modification times.
//
// WoCloud reports a modification time but there is no API to set it, so
// advertising a real precision would make rclone re-upload every file on
// the next sync. Report ModTimeNotSupported so that sync compares size and
// hash instead, while still returning the server time from ModTime.
func (f *Fs) Precision() time.Duration {
	return fs.ModTimeNotSupported
}

// Hashes returns the hash types supported by this Fs.
//
// The download endpoint returns the MD5 in its ETag header. This costs an
// extra request per object so it is marked as SlowHash.
func (f *Fs) Hashes() hash.Set {
	return hash.NewHashSet(hash.MD5)
}

// parsePath normalizes a remote root path
func parsePath(p string) string {
	return strings.Trim(p, "/")
}

// shouldRetry returns whether the request should be retried
func shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	return fserrors.ShouldRetry(err) || fserrors.ShouldRetryHTTP(resp, retryErrorCodes), err
}

// retryErrorCodes is a slice of HTTP codes which are retried
var retryErrorCodes = []int{
	429, // Too Many Requests
	500, // Internal Server Error
	502, // Bad Gateway
	503, // Service Unavailable
	504, // Gateway Timeout
}

// createLimiter paces directory creation.
//
// The service starts returning 9999 (system exception) after roughly 30
// directory creations. This is a global limiter so that all the Fs
// instances in this process share it. Uploads and other operations are
// not affected.
var createLimiter = rate.NewLimiter(rate.Every(2500*time.Millisecond), 5)

// apiError is an error returned by the dispatcher in the RSP envelope.
type apiError struct {
	Op   string
	Code string
	Desc string
}

// Error implements the error interface.
func (e *apiError) Error() string {
	if e.Desc != "" {
		return fmt.Sprintf("wocloud: %s: %s (code %s)", e.Op, e.Desc, e.Code)
	}
	return fmt.Sprintf("wocloud: %s: error code %s", e.Op, e.Code)
}

// isAPIError returns true if err is an apiError with one of the codes
func isAPIError(err error, codes ...string) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, code := range codes {
		if apiErr.Code == code {
			return true
		}
	}
	return false
}

// isAuthError returns true if err means the access token is invalid
func isAuthError(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == "1001" ||
		strings.Contains(apiErr.Desc, "登录") ||
		strings.Contains(apiErr.Desc, "令牌")
}

// uploadErrorHandler parses the non-standard upload error response
func uploadErrorHandler(resp *http.Response) error {
	body, err := rest.ReadBody(resp)
	if len(body) > 0 {
		var uploadResp wapi.UploadResponse
		if json.Unmarshal(body, &uploadResp) == nil && uploadResp.Code != "" {
			return &apiError{Op: "upload2C", Code: uploadResp.Code, Desc: uploadResp.Msg}
		}
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("wocloud: upload failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// newDispatcher makes a dispatcher for the given host and credentials
func newDispatcher(ctx context.Context, apiHost, clientID, clientSecret, token string) *dispatcher {
	srv := rest.NewClient(fshttp.NewClient(ctx)).SetRoot(strings.TrimRight(apiHost, "/"))
	srv.SetHeader("accesstoken", token)
	return &dispatcher{
		srv:          srv,
		pacer:        fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
		clientID:     clientID,
		clientSecret: clientSecret,
		token:        token,
	}
}

// aesKey returns the first 16 bytes of key as an AES key
func aesKey(key string) ([]byte, error) {
	if len(key) < aes.BlockSize {
		return nil, fmt.Errorf("wocloud: key too short (need at least %d bytes)", aes.BlockSize)
	}
	return []byte(key[:aes.BlockSize]), nil
}

// pkcs7Pad pads data to a multiple of blockSize
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padText := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padText...)
}

// pkcs7Unpad removes PKCS#7 padding
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("wocloud: invalid padded data length")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize {
		return nil, errors.New("wocloud: invalid padding")
	}
	for _, b := range data[len(data)-padding:] {
		if int(b) != padding {
			return nil, errors.New("wocloud: invalid padding")
		}
	}
	return data[:len(data)-padding], nil
}

// aesEncrypt encrypts plain with AES-128-CBC/PKCS7 and returns base64
func aesEncrypt(plain, key string) (string, error) {
	keyBytes, err := aesKey(key)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return "", fmt.Errorf("wocloud: failed to make AES cipher: %w", err)
	}
	padded := pkcs7Pad([]byte(plain), aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(aesIV)).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out), nil
}

// aesDecrypt decrypts base64 encoded AES-128-CBC/PKCS7 data
func aesDecrypt(encoded, key string) ([]byte, error) {
	keyBytes, err := aesKey(key)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Some responses omit the padding.
		data, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("wocloud: invalid base64: %w", err)
		}
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("wocloud: failed to make AES cipher: %w", err)
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("wocloud: ciphertext is not a multiple of the block size")
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, []byte(aesIV)).CryptBlocks(out, data)
	return pkcs7Unpad(out, aes.BlockSize)
}

// makeSign makes the dispatcher signature
func makeSign(op string, resTime int64, reqSeq int, channel string, version string) string {
	sum := md5.Sum([]byte(op + strconv.FormatInt(resTime, 10) + strconv.Itoa(reqSeq) + channel + version))
	return hex.EncodeToString(sum[:])
}

// decodeData decrypts the DATA field of a response if it is a string
func (d *dispatcher) decodeData(channel string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] != '"' {
		return raw, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return raw, nil
	}
	if encoded == "" {
		return nil, nil
	}
	var (
		dec    []byte
		decErr error
	)
	if channel == channelWoHome {
		if d.token != "" {
			dec, decErr = aesDecrypt(encoded, d.token)
		}
		if len(dec) == 0 {
			dec, decErr = aesDecrypt(encoded, d.clientSecret)
		}
	} else {
		dec, decErr = aesDecrypt(encoded, d.clientSecret)
		if len(dec) == 0 && json.Valid([]byte(encoded)) {
			// api-user responses are normally plaintext JSON.
			dec = []byte(encoded)
			decErr = nil
		}
	}
	if len(dec) == 0 {
		return nil, fmt.Errorf("wocloud: failed to decrypt response data: %w", decErr)
	}
	if !json.Valid(dec) {
		return nil, errors.New("wocloud: decrypted response data is not valid JSON")
	}
	return json.RawMessage(dec), nil
}

// call runs one dispatcher operation and returns its decrypted DATA.
//
// Transient 9999 (system exception) errors are retried a couple of times.
func (d *dispatcher) call(ctx context.Context, channel, op string, params any) (json.RawMessage, error) {
	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		data, err := d.callOnce(ctx, channel, op, params)
		if err == nil {
			return data, nil
		}
		if isAPIError(err, "9999") && attempt < maxAttempts-1 {
			delay := 200 * time.Millisecond * (1 << attempt)
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}
			fs.Debugf(nil, "wocloud: retrying %s after temporary error 9999 in %v", op, delay)
			if sleepErr := sleepContext(ctx, delay); sleepErr != nil {
				return nil, sleepErr
			}
			continue
		}
		return nil, err
	}
	return nil, errors.New("wocloud: internal error: retry loop exited")
}

// callOnce runs one dispatcher operation without retrying API errors.
func (d *dispatcher) callOnce(ctx context.Context, channel, op string, params any) (json.RawMessage, error) {
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("wocloud: failed to marshal %s parameters: %w", op, err)
	}

	var body wapi.Body
	var key string
	if channel == channelWoHome {
		// The web client injects the client ID into the encrypted payload.
		var fields map[string]any
		if err := json.Unmarshal(payload, &fields); err != nil {
			return nil, fmt.Errorf("wocloud: failed to parse %s parameters: %w", op, err)
		}
		fields["clientId"] = d.clientID
		payload, err = json.Marshal(fields)
		if err != nil {
			return nil, fmt.Errorf("wocloud: failed to marshal %s parameters: %w", op, err)
		}
		key = d.token
		if key == "" {
			key = d.clientSecret
		}
		body.Key = true
	} else {
		key = d.clientSecret
		body.ClientID = d.clientID
		body.Secret = true
	}

	body.Param, err = aesEncrypt(string(payload), key)
	if err != nil {
		return nil, err
	}

	resTime := time.Now().UnixMilli()
	reqSeq := rand.IntN(90000) + 100000
	request := wapi.Request{
		Header: wapi.Header{
			Key:     op,
			ResTime: resTime,
			ReqSeq:  reqSeq,
			Channel: channel,
			Sign:    makeSign(op, resTime, reqSeq, channel, ""),
			Version: "",
		},
		Body: body,
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("wocloud: failed to marshal request: %w", err)
	}
	opts := rest.Opts{
		Method:      "POST",
		Path:        "/" + channel + "/dispatcher",
		ContentType: "application/json",
	}

	var (
		resp   *http.Response
		result wapi.Response
	)
	err = d.pacer.Call(func() (bool, error) {
		opts.Body = bytes.NewReader(requestJSON)
		result = wapi.Response{}
		resp, err = d.srv.CallJSON(ctx, &opts, nil, &result)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	if result.RSP.Code == "" && result.RSP.Desc == "" {
		return nil, fmt.Errorf("wocloud: %s: unexpected response with no RSP field", op)
	}
	if result.RSP.Code != "0000" {
		return nil, &apiError{Op: op, Code: result.RSP.Code, Desc: result.RSP.Desc}
	}
	return d.decodeData(channel, result.RSP.Data)
}

// ------------------------------------------------------------

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	if err := configstruct.Set(m, opt); err != nil {
		return nil, err
	}

	if opt.AccessToken == "" {
		return nil, errors.New(`wocloud: access_token not set - run "rclone config reconnect <remote>:"`)
	}
	switch opt.SpaceType {
	case "":
		opt.SpaceType = "0"
	case "0", "1", "4":
	default:
		return nil, fmt.Errorf("wocloud: invalid space_type %q (must be 0, 1 or 4)", opt.SpaceType)
	}
	if opt.SpaceType == "1" && opt.FamilyID == "" {
		return nil, errors.New("wocloud: family_id is required when space_type is 1")
	}
	if opt.SpaceType == "4" && opt.PsToken == "" {
		return nil, errors.New("wocloud: ps_token is required when space_type is 4")
	}
	if opt.APIHost == "" {
		opt.APIHost = defaultAPIHost
	}
	if opt.ClientID == "" {
		opt.ClientID = defaultClientID
	}
	if opt.ClientSecret == "" {
		opt.ClientSecret = defaultClientSecret
	}
	if opt.PageSize <= 0 || opt.PageSize > maxPageSize {
		opt.PageSize = defaultPageSize
	}
	if opt.PollTimeout < 0 {
		opt.PollTimeout = 0
	}
	if opt.RootFolderID == "" {
		opt.RootFolderID = rootID
	}

	root = parsePath(root)
	f := &Fs{
		name: name,
		root: root,
		opt:  *opt,
	}
	f.disp = newDispatcher(ctx, opt.APIHost, opt.ClientID, opt.ClientSecret, opt.AccessToken)
	f.features = (&fs.Features{
		CanHaveEmptyDirectories: true,
		Purge:                   f.Purge,
		DirCacheFlush:           f.dirCacheFlush,
		SlowHash:                true,
	}).Fill(ctx, f)
	f.dirCache = dircache.New(root, opt.RootFolderID, f)

	err := f.dirCache.FindRoot(ctx, false)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, fs.ErrorDirNotFound) {
		return nil, err
	}

	// The root path doesn't exist as a directory. It may be a file.
	newRoot, remote := dircache.SplitPath(root)
	tempF := &Fs{
		name: f.name,
		root: newRoot,
		opt:  f.opt,
		disp: f.disp,
	}
	tempF.dirCache = dircache.New(newRoot, opt.RootFolderID, tempF)
	if err = tempF.dirCache.FindRoot(ctx, false); err != nil {
		// No parent directory so return the original Fs
		return f, nil
	}
	if _, err = tempF.newObjectWithInfo(ctx, remote, nil); err != nil {
		if errors.Is(err, fs.ErrorObjectNotFound) {
			return f, nil
		}
		return nil, err
	}
	// Update the original Fs rather than returning tempF, since the
	// feature functions were filled with *f as a receiver.
	f.dirCache = tempF.dirCache
	f.root = tempF.root
	return f, fs.ErrorIsFile
}

// dirCacheFlush flushes the directory cache
func (f *Fs) dirCacheFlush() {
	f.dirCache.Flush()
}

// spaceParams returns the common space selection parameters
func (f *Fs) spaceParams() wapi.SpaceParams {
	return wapi.SpaceParams{
		SpaceType: f.opt.SpaceType,
		FamilyID:  f.opt.FamilyID,
		PsToken:   f.opt.PsToken,
	}
}

// listPage lists one page of a directory
func (f *Fs) listPage(ctx context.Context, dirID string, pageNum int) ([]wapi.Item, error) {
	params := &wapi.ListParams{
		SpaceParams:       f.spaceParams(),
		ParentDirectoryID: dirID,
		PageNum:           pageNum,
		PageSize:          f.opt.PageSize,
		SortRule:          sortRule,
	}
	raw, err := f.disp.call(ctx, channelWoHome, "QueryAllFiles", params)
	if err != nil {
		return nil, err
	}
	var result wapi.ListResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("wocloud: failed to parse QueryAllFiles: %w", err)
	}
	return result.Files, nil
}

// listAll lists all the items in a directory calling fn for each of them.
//
// If fn returns true then listing stops immediately.
func (f *Fs) listAll(ctx context.Context, dirID string, fn func(wapi.Item) bool) error {
	for page := 0; page < maxPages; page++ {
		items, err := f.listPage(ctx, dirID, page)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			if fn(item) {
				return nil
			}
		}
		if len(items) < f.opt.PageSize {
			return nil
		}
	}
	return errors.New("wocloud: too many listing pages")
}

// FindLeaf finds a directory of name leaf in the folder with ID pathID
func (f *Fs) FindLeaf(ctx context.Context, pathID, leaf string) (pathIDOut string, found bool, err error) {
	err = f.listAll(ctx, pathID, func(item wapi.Item) bool {
		if int64(item.Type) != int64(wapi.TypeDirectory) {
			return false
		}
		if f.opt.Enc.ToStandardName(item.Name) == leaf {
			pathIDOut = item.ID
			found = true
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

// CreateDir makes a directory with pathID as parent and name leaf
func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (newID string, err error) {
	params := &wapi.CreateDirectoryParams{
		IsCouldRepeat:     "0",
		SpaceParams:       f.spaceParams(),
		ParentDirectoryID: pathID,
		DirectoryName:     f.opt.Enc.FromStandardName(leaf),
	}
	if err := createLimiter.Wait(ctx); err != nil {
		return "", err
	}
	raw, err := f.disp.call(ctx, channelWoHome, "CreateDirectory", params)
	if err != nil {
		// 130007 means the folder already exists. 9999 is returned if two
		// requests try to create the same directory at once, so check
		// whether it exists before giving up.
		if isAPIError(err, "130007", "9999") {
			if id, found, findErr := f.FindLeaf(ctx, pathID, leaf); findErr == nil && found {
				return id, nil
			}
		}
		return "", err
	}
	var data wapi.CreateDirectoryData
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", fmt.Errorf("wocloud: failed to parse CreateDirectory: %w", err)
	}
	if data.ID == "" {
		return "", errors.New("wocloud: CreateDirectory returned no id")
	}
	return data.ID, nil
}

// List the objects and directories in dir into entries
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return nil, err
	}
	err = f.listAll(ctx, dirID, func(item wapi.Item) bool {
		name := f.opt.Enc.ToStandardName(item.Name)
		remote := path.Join(dir, name)
		switch int64(item.Type) {
		case int64(wapi.TypeDirectory):
			f.dirCache.Put(remote, item.ID)
			entries = append(entries, fs.NewDir(remote, parseTime(string(item.ModifyTime))).SetID(item.ID))
		case int64(wapi.TypeFile):
			entries = append(entries, f.newObject(remote, item))
		default:
			fs.Debugf(f, "Ignoring %q with unknown type %d", name, item.Type)
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// newObject makes an Object from a listing item
func (f *Fs) newObject(remote string, item wapi.Item) *Object {
	o := &Object{fs: f, remote: remote}
	o.setMeta(item)
	return o
}

// setMeta sets the metadata from a listing item
func (o *Object) setMeta(item wapi.Item) {
	o.size = int64(item.Size)
	o.modTime = parseTime(string(item.ModifyTime))
	o.id = item.ID
	o.fid = item.Fid
	o.dirID = item.ParentDirectoryID
	o.fileType = string(item.FileType)
	o.fileVersion = int64(item.FileVersion)
}

// newObjectWithInfo creates a half finished Object which must have
// setMetaData called on it if info is nil.
func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, info *wapi.Item) (fs.Object, error) {
	o := &Object{fs: f, remote: remote}
	if info != nil {
		o.setMeta(*info)
		return o, nil
	}
	item, err := f.findFile(ctx, "", remote, false)
	if err != nil {
		return nil, err
	}
	o.setMeta(*item)
	return o, nil
}

// NewObject finds the Object at remote
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	leaf, dirID, err := f.dirCache.FindPath(ctx, remote, false)
	if err != nil {
		if errors.Is(err, fs.ErrorDirNotFound) {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	var file *wapi.Item
	var isDir bool
	err = f.listAll(ctx, dirID, func(item wapi.Item) bool {
		if f.opt.Enc.ToStandardName(item.Name) != leaf {
			return false
		}
		switch int64(item.Type) {
		case int64(wapi.TypeFile):
			it := item
			file = &it
			return true
		case int64(wapi.TypeDirectory):
			isDir = true
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if file != nil {
		return f.newObject(remote, *file), nil
	}
	if isDir {
		return nil, fs.ErrorIsDir
	}
	return nil, fs.ErrorObjectNotFound
}

// findFile finds a file by name in dirID. If dirID is empty it resolves
// the parent directory from remote. If wait is set it polls until the
// file appears or PollTimeout expires.
func (f *Fs) findFile(ctx context.Context, dirID, remote string, wait bool) (*wapi.Item, error) {
	leaf := remote
	parent := ""
	if dirID == "" {
		var err error
		leaf, dirID, err = f.dirCache.FindPath(ctx, remote, false)
		if err != nil {
			if errors.Is(err, fs.ErrorDirNotFound) {
				return nil, fs.ErrorObjectNotFound
			}
			return nil, err
		}
	}
	parent = dirID
	lookup := func() (*wapi.Item, error) {
		var found *wapi.Item
		err := f.listAll(ctx, parent, func(item wapi.Item) bool {
			if int64(item.Type) != int64(wapi.TypeFile) {
				return false
			}
			if f.opt.Enc.ToStandardName(item.Name) == leaf {
				it := item
				found = &it
				return true
			}
			return false
		})
		return found, err
	}
	item, err := lookup()
	if err != nil {
		return nil, err
	}
	if item != nil {
		return item, nil
	}
	if !wait || f.opt.PollTimeout <= 0 {
		return nil, fs.ErrorObjectNotFound
	}
	deadline := time.Now().Add(time.Duration(f.opt.PollTimeout))
	for {
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("wocloud: timed out waiting for %q to appear", leaf)
		}
		if err := sleepContext(ctx, 200*time.Millisecond); err != nil {
			return nil, err
		}
		item, err = lookup()
		if err != nil {
			return nil, err
		}
		if item != nil {
			return item, nil
		}
	}
}

// Mkdir makes the directory (container, bucket)
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	_, err := f.dirCache.FindDir(ctx, dir, true)
	return err
}

// Put the object
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	return f.put(ctx, in, src, src.Remote())
}

// put uploads in to remote
func (f *Fs) put(ctx context.Context, in io.Reader, src fs.ObjectInfo, remote string) (fs.Object, error) {
	leaf, dirID, err := f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return nil, err
	}

	size := src.Size()
	cleanup := func() {}
	if size < 0 {
		in, size, cleanup, err = spoolToTemp(in)
		if err != nil {
			return nil, err
		}
		defer cleanup()
	}
	if size == 0 {
		return nil, fs.ErrorCantUploadEmptyFiles
	}

	data, err := f.upload(ctx, in, size, leaf, dirID)
	if err != nil {
		return nil, err
	}

	o := &Object{
		fs:          f,
		remote:      remote,
		size:        size,
		modTime:     time.Now(),
		id:          data.WcFileID,
		fid:         data.Fid,
		dirID:       dirID,
		fileType:    fileTypeOf(leaf),
		fileVersion: parseInt(string(data.FileVersion)),
	}
	if o.id == "" || o.fid == "" {
		item, err := f.findFile(ctx, dirID, leaf, true)
		if err != nil {
			return nil, err
		}
		o = f.newObject(remote, *item)
	}
	return o, nil
}

// upload uploads one object to directory dirID using upload2C
func (f *Fs) upload(ctx context.Context, in io.Reader, size int64, leaf, dirID string) (*wapi.UploadData, error) {
	name := f.opt.Enc.FromStandardName(leaf)
	batchNo := strings.ReplaceAll(uuid.NewString(), "-", "")
	uniqueID := fmt.Sprintf("%d_%s", time.Now().UnixMilli(), randomHex(6))
	fileType := fileTypeOf(leaf)

	fileInfoJSON, err := json.Marshal(&wapi.UploadFileInfo{
		FileName:    name,
		FileSize:    size,
		FileType:    fileType,
		DirectoryID: dirID,
		BatchNo:     batchNo,
		SpaceType:   f.opt.SpaceType,
	})
	if err != nil {
		return nil, fmt.Errorf("wocloud: failed to marshal fileInfo: %w", err)
	}
	encryptedInfo, err := aesEncrypt(string(fileInfoJSON), f.opt.AccessToken)
	if err != nil {
		return nil, err
	}

	params := url.Values{
		"uniqueId":    {uniqueID},
		"accessToken": {f.opt.AccessToken},
		"fileName":    {name},
		"psToken":     {f.opt.PsToken},
		"fileSize":    {strconv.FormatInt(size, 10)},
		"totalPart":   {"1"},
		"partSize":    {strconv.FormatInt(size, 10)},
		"partIndex":   {"1"},
		"channel":     {"wocloud"},
		"directoryId": {dirID},
		"fileInfo":    {encryptedInfo},
	}

	uploadSrv, err := f.getUploadClient(ctx)
	if err != nil {
		return nil, err
	}

	contentLength := size
	opts := rest.Opts{
		Method:               "POST",
		Path:                 uploadPath,
		Body:                 in,
		ContentLength:        &contentLength,
		MultipartParams:      params,
		MultipartContentName: "file",
		MultipartFileName:    "blob",
		MultipartContentType: "application/octet-stream",
	}

	var (
		resp   *http.Response
		result wapi.UploadResponse
	)
	err = f.disp.pacer.CallNoRetry(func() (bool, error) {
		result = wapi.UploadResponse{}
		resp, err = uploadSrv.CallJSON(ctx, &opts, nil, &result)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		if isAPIError(err, "130004") {
			return nil, fmt.Errorf("%w: %w", fs.ErrorDirNotFound, err)
		}
		return nil, err
	}
	if result.Code != "0000" {
		return nil, &apiError{Op: "upload2C", Code: result.Code, Desc: result.Msg}
	}
	return &result.Data, nil
}

// getUploadClient returns a lazy client for the upload host
func (f *Fs) getUploadClient(ctx context.Context) (*rest.Client, error) {
	f.uploadMu.Lock()
	defer f.uploadMu.Unlock()
	if f.uploadSrv != nil {
		return f.uploadSrv, nil
	}
	host := strings.TrimRight(f.opt.UploadHost, "/")
	if host == "" {
		raw, err := f.disp.call(ctx, channelWoHome, "GetZoneInfo", &wapi.ZoneInfoParams{AppID: "10000001"})
		if err == nil {
			var zone wapi.ZoneInfo
			if json.Unmarshal(raw, &zone) == nil {
				host = strings.TrimRight(zone.URL, "/")
			}
		}
		if host == "" {
			fs.Debugf(f, "Failed to discover upload host, using %s: %v", defaultUploadHost, err)
			host = defaultUploadHost
		}
	}
	f.uploadHost = host
	srv := rest.NewClient(fshttp.NewClient(ctx)).SetRoot(host)
	srv.SetHeader("accesstoken", f.opt.AccessToken)
	srv.SetErrorHandler(uploadErrorHandler)
	f.uploadSrv = srv
	return srv, nil
}

// getDownloadURL returns a download URL for fid
func (f *Fs) getDownloadURL(ctx context.Context, fid string) (string, error) {
	if fid == "" {
		return "", errors.New("wocloud: object has no fid")
	}
	params := &wapi.DownloadParams{
		FidList:   []string{fid},
		ClientID:  f.opt.ClientID,
		SpaceType: f.opt.SpaceType,
	}
	raw, err := f.disp.call(ctx, channelWoHome, "GetDownloadUrl", params)
	if err != nil {
		return "", err
	}
	var items []wapi.DownloadItem
	if err := json.Unmarshal(raw, &items); err == nil && len(items) > 0 && items[0].DownloadURL != "" {
		return items[0].DownloadURL, nil
	}
	var item wapi.DownloadItem
	if err := json.Unmarshal(raw, &item); err == nil && item.DownloadURL != "" {
		return item.DownloadURL, nil
	}
	return "", errors.New("wocloud: GetDownloadUrl returned no download URL")
}

// deleteBatch deletes one batch of files and directories
func (f *Fs) deleteBatch(ctx context.Context, fileIDs, dirIDs []string) error {
	if len(fileIDs) == 0 && len(dirIDs) == 0 {
		return nil
	}
	if fileIDs == nil {
		fileIDs = []string{}
	}
	if dirIDs == nil {
		dirIDs = []string{}
	}
	params := &wapi.DeleteParams{
		SpaceParams: f.spaceParams(),
		VipLevel:    "0",
		FileList:    fileIDs,
		DirList:     dirIDs,
	}
	_, err := f.disp.call(ctx, channelWoHome, "DeleteFile", params)
	return err
}

// deleteIDs deletes IDs in batches
func (f *Fs) deleteIDs(ctx context.Context, fileIDs, dirIDs []string) error {
	for len(fileIDs) > 0 || len(dirIDs) > 0 {
		nf := len(fileIDs)
		if nf > maxDeleteBatch {
			nf = maxDeleteBatch
		}
		nd := len(dirIDs)
		if nd > maxDeleteBatch {
			nd = maxDeleteBatch
		}
		if err := f.deleteBatch(ctx, fileIDs[:nf], dirIDs[:nd]); err != nil {
			return err
		}
		fileIDs = fileIDs[nf:]
		dirIDs = dirIDs[nd:]
	}
	return nil
}

// Rmdir removes the directory if it is empty
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	if dir == "" {
		return f.removeRootDir(ctx, true)
	}
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	var notEmpty bool
	err = f.listAll(ctx, dirID, func(item wapi.Item) bool {
		notEmpty = true
		return true
	})
	if err != nil {
		return err
	}
	if notEmpty {
		return fs.ErrorDirectoryNotEmpty
	}
	parentPath, _ := dircache.SplitPath(dir)
	parentID, err := f.dirCache.FindDir(ctx, parentPath, false)
	if err != nil {
		return err
	}
	if err := f.deleteBatch(ctx, nil, []string{dirID}); err != nil {
		return err
	}
	if err := f.waitForGone(ctx, parentID, dirID); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

// removeRootDir removes the directory the Fs is rooted at.
//
// This is used for Rmdir("") and Purge("") when the remote path pointed
// at a subdirectory. The account root itself can't be removed.
func (f *Fs) removeRootDir(ctx context.Context, checkEmpty bool) error {
	if f.root == "" {
		return errors.New("wocloud: can't remove the root directory")
	}
	rootDirID, err := f.dirCache.RootID(ctx, false)
	if err != nil {
		return err
	}
	parentID, err := f.dirCache.RootParentID(ctx, false)
	if err != nil {
		return err
	}
	if checkEmpty {
		var notEmpty bool
		err = f.listAll(ctx, rootDirID, func(item wapi.Item) bool {
			notEmpty = true
			return true
		})
		if err != nil {
			return err
		}
		if notEmpty {
			return fs.ErrorDirectoryNotEmpty
		}
	}
	if err := f.deleteBatch(ctx, nil, []string{rootDirID}); err != nil {
		return err
	}
	if err := f.waitForGone(ctx, parentID, rootDirID); err != nil {
		return err
	}
	f.dirCache.ResetRoot()
	return nil
}

// Purge deletes all the files in the directory and the directory itself
func (f *Fs) Purge(ctx context.Context, dir string) error {
	if dir == "" {
		if f.root == "" {
			// The account root can't be deleted, so empty it instead.
			return f.purgeRoot(ctx)
		}
		return f.removeRootDir(ctx, false)
	}
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	parentPath, _ := dircache.SplitPath(dir)
	parentID, err := f.dirCache.FindDir(ctx, parentPath, false)
	if err != nil {
		return err
	}
	if err := f.deleteBatch(ctx, nil, []string{dirID}); err != nil {
		return err
	}
	if err := f.waitForGone(ctx, parentID, dirID); err != nil {
		return err
	}
	f.dirCache.FlushDir(dir)
	return nil
}

// purgeRoot deletes all the entries in the configured root
func (f *Fs) purgeRoot(ctx context.Context) error {
	rootDirID, err := f.dirCache.RootID(ctx, false)
	if err != nil {
		return err
	}
	var fileIDs, dirIDs []string
	err = f.listAll(ctx, rootDirID, func(item wapi.Item) bool {
		switch int64(item.Type) {
		case int64(wapi.TypeDirectory):
			dirIDs = append(dirIDs, item.ID)
		case int64(wapi.TypeFile):
			fileIDs = append(fileIDs, item.ID)
		}
		return false
	})
	if err != nil {
		return err
	}
	if err := f.deleteIDs(ctx, fileIDs, dirIDs); err != nil {
		return err
	}
	if err := f.waitForEmpty(ctx, rootDirID); err != nil {
		return err
	}
	f.dirCache.FlushDir("")
	return nil
}

// waitForGone waits until id is no longer present in dirID
func (f *Fs) waitForGone(ctx context.Context, dirID, id string) error {
	timeout := time.Duration(f.opt.PollTimeout)
	if timeout <= 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	sleep := 200 * time.Millisecond
	for {
		found := false
		err := f.listAll(ctx, dirID, func(item wapi.Item) bool {
			if item.ID == id {
				found = true
				return true
			}
			return false
		})
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("wocloud: timed out after %v waiting for %s to be deleted (increase --wocloud-poll-timeout)", timeout, id)
		}
		if err := sleepContext(ctx, sleep); err != nil {
			return err
		}
		sleep *= 2
		if sleep > 2*time.Second {
			sleep = 2 * time.Second
		}
	}
}

// waitForEmpty waits until dirID has no entries
func (f *Fs) waitForEmpty(ctx context.Context, dirID string) error {
	timeout := time.Duration(f.opt.PollTimeout)
	if timeout <= 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	sleep := 200 * time.Millisecond
	for {
		found := false
		err := f.listAll(ctx, dirID, func(item wapi.Item) bool {
			found = true
			return true
		})
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("wocloud: timed out after %v waiting for the directory to become empty (increase --wocloud-poll-timeout)", timeout)
		}
		if err := sleepContext(ctx, sleep); err != nil {
			return err
		}
		sleep *= 2
		if sleep > 2*time.Second {
			sleep = 2 * time.Second
		}
	}
}

// sleepContext sleeps for d or until the context is cancelled
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// spoolToTemp copies in to a temporary file so its size is known
func spoolToTemp(in io.Reader) (io.Reader, int64, func(), error) {
	tmp, err := os.CreateTemp("", "rclone-wocloud-upload-*")
	if err != nil {
		return nil, 0, nil, fmt.Errorf("wocloud: failed to make temporary file: %w", err)
	}
	size, err := io.Copy(tmp, in)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, 0, nil, fmt.Errorf("wocloud: failed to spool upload: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, 0, nil, fmt.Errorf("wocloud: failed to rewind temporary file: %w", err)
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	return tmp, size, cleanup, nil
}

// randomHex returns n random hex characters
func randomHex(n int) string {
	s := strings.ReplaceAll(uuid.NewString(), "-", "")
	if n > len(s) {
		n = len(s)
	}
	return s[:n]
}

// parseInt parses a decimal string to int64, returning 0 on error
func parseInt(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}

// parseTime parses a WoCloud YYYYMMDDHHMMSS timestamp
func parseTime(s string) time.Time {
	if len(s) != 14 {
		return time.Time{}
	}
	t, err := time.ParseInLocation("20060102150405", s, chinaTime)
	if err != nil {
		return time.Time{}
	}
	return t
}

// fileTypeOf returns the WoCloud category for a file name:
// 1 image, 2 video, 3 audio, 4 document, 5 other.
func fileTypeOf(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	switch {
	case imageExts[ext]:
		return "1"
	case videoExts[ext]:
		return "2"
	case audioExts[ext]:
		return "3"
	case docExts[ext]:
		return "4"
	default:
		return "5"
	}
}

var (
	imageExts = map[string]bool{
		"jpg": true, "png": true, "bmp": true, "jpeg": true, "tif": true,
		"tiff": true, "psd": true, "tga": true, "raw": true, "pcd": true,
		"heic": true, "ico": true, "livp": true, "webp": true, "gif": true,
	}
	videoExts = map[string]bool{
		"avi": true, "asf": true, "m4v": true, "dat": true, "3gp": true,
		"m3u8": true, "m3us": true, "dv": true, "flv": true, "mkv": true,
		"webm": true, "mov": true, "ogv": true, "mp4": true, "rm": true,
		"swf": true, "ts": true, "vob": true, "wmv": true, "rmvb": true,
		"mpg": true,
	}
	audioExts = map[string]bool{
		"au": true, "ac3": true, "flac": true, "m4a": true, "mp2": true,
		"mp3": true, "wav": true, "wma": true, "ape": true, "mpc": true,
		"tta": true, "ogg": true, "amr": true, "aac": true, "aiff": true,
		"mka": true, "wv": true,
	}
	docExts = map[string]bool{
		"txt": true, "doc": true, "docx": true, "ppt": true, "pptx": true,
		"xls": true, "xlsx": true, "pdf": true, "rtf": true, "hlp": true,
		"md": true, "text": true,
	}
)

// ------------------------------------------------------------

// Fs returns read only access to the Fs
func (o *Object) Fs() fs.Info {
	return o.fs
}

// Remote returns the remote path
func (o *Object) Remote() string {
	return o.remote
}

// String returns a description of the object
func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

// ModTime returns the modification time of the object
func (o *Object) ModTime(ctx context.Context) time.Time {
	return o.modTime
}

// Size returns the size of an object in bytes
func (o *Object) Size() int64 {
	return o.size
}

// SetModTime is not supported by WoCloud
func (o *Object) SetModTime(ctx context.Context, modTime time.Time) error {
	return fs.ErrorCantSetModTime
}

// Storable returns whether this object is storable
func (o *Object) Storable() bool {
	return true
}

// Hash returns the MD5 of the object.
//
// The list response doesn't include a hash so this fetches the download
// URL and reads the ETag from a HEAD request.
func (o *Object) Hash(ctx context.Context, ty hash.Type) (string, error) {
	if ty != hash.MD5 {
		return "", hash.ErrUnsupported
	}
	if o.md5 != "" {
		return o.md5, nil
	}
	if o.fid == "" {
		return "", hash.ErrUnsupported
	}
	downloadURL, err := o.fs.getDownloadURL(ctx, o.fid)
	if err != nil {
		return "", err
	}
	opts := rest.Opts{Method: "HEAD", RootURL: downloadURL, NoResponse: true}
	var resp *http.Response
	err = o.fs.disp.pacer.Call(func() (bool, error) {
		resp, err = o.fs.disp.srv.Call(ctx, &opts)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if len(etag) != 32 {
		return "", hash.ErrUnsupported
	}
	if _, err := hex.DecodeString(etag); err != nil {
		return "", hash.ErrUnsupported
	}
	o.md5 = strings.ToLower(etag)
	return o.md5, nil
}

// Open an object for read
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	downloadURL, err := o.fs.getDownloadURL(ctx, o.fid)
	if err != nil {
		return nil, err
	}
	fs.FixRangeOption(options, o.size)
	opts := rest.Opts{
		Method:  "GET",
		RootURL: downloadURL,
		Options: options,
	}
	var resp *http.Response
	err = o.fs.disp.pacer.Call(func() (bool, error) {
		resp, err = o.fs.disp.srv.Call(ctx, &opts)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Update the object with the contents of the io.Reader
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	newObj, err := o.fs.put(ctx, in, src, o.Remote())
	if err != nil {
		return err
	}
	updated, ok := newObj.(*Object)
	if !ok {
		return errors.New("wocloud: internal error: Put returned the wrong type")
	}
	o.size = updated.size
	o.modTime = updated.modTime
	o.id = updated.id
	o.fid = updated.fid
	o.dirID = updated.dirID
	o.fileType = updated.fileType
	o.fileVersion = updated.fileVersion
	o.md5 = ""
	return nil
}

// Remove this object
func (o *Object) Remove(ctx context.Context) error {
	if o.id == "" {
		return errors.New("wocloud: object has no id")
	}
	if err := o.fs.deleteBatch(ctx, []string{o.id}, nil); err != nil {
		return err
	}
	if o.dirID == "" {
		return nil
	}
	return o.fs.waitForGone(ctx, o.dirID, o.id)
}

// ID returns the ID of the Object if known, or "" if not
func (o *Object) ID() string {
	return o.id
}

// ParentID returns the ID of the Object parent if known, or "" if not
func (o *Object) ParentID() string {
	return o.dirID
}

// ------------------------------------------------------------

// Config runs the interactive configuration for the backend.
//
// The login needs a phone number, a password, a graphical captcha and an
// SMS code. The temporary values are kept in config_ keys which are
// ephemeral and are never written to the config file.
func Config(ctx context.Context, name string, m configmap.Mapper, configIn fs.ConfigIn) (*fs.ConfigOut, error) {
	opt := new(Options)
	if err := configstruct.Set(m, opt); err != nil {
		return nil, fmt.Errorf("wocloud: failed to read options: %w", err)
	}
	apiHost := strings.TrimRight(opt.APIHost, "/")
	if apiHost == "" {
		apiHost = defaultAPIHost
	}
	clientID := opt.ClientID
	if clientID == "" {
		clientID = defaultClientID
	}
	clientSecret := opt.ClientSecret
	if clientSecret == "" {
		clientSecret = defaultClientSecret
	}
	d := newDispatcher(ctx, apiHost, clientID, clientSecret, "")

	switch configIn.State {
	case "":
		if opt.AccessToken != "" {
			return fs.ConfigConfirm("refresh", true, "config_refresh", "Access token already configured - replace it?")
		}
		return fs.ConfigGoto("phone")
	case "refresh":
		if configIn.Result == "false" {
			return nil, nil
		}
		return fs.ConfigGoto("phone")
	case "phone":
		return fs.ConfigInput("password", "config_phone", "Phone number of the WoCloud account")
	case "password":
		if configIn.Result != "" {
			m.Set("config_phone", configIn.Result)
		}
		phone, _ := m.Get("config_phone")
		if phone == "" {
			return fs.ConfigError("phone", "Phone number can't be blank")
		}
		return fs.ConfigPassword("login", "config_password", "WoCloud password (only used for this login and not stored)")
	case "login":
		if configIn.Result != "" {
			m.Set("config_password", configIn.Result)
		}
		phone, _ := m.Get("config_phone")
		encPassword, _ := m.Get("config_password")
		if phone == "" || encPassword == "" {
			return nil, errors.New(`wocloud: interactive login state was lost - run "rclone config reconnect <remote>:" in a terminal, or supply access_token`)
		}
		password, err := obscure.Reveal(encPassword)
		if err != nil {
			return nil, fmt.Errorf("wocloud: failed to reveal password: %w", err)
		}
		uuidStr := uuid.NewString()
		m.Set("config_uuid", uuidStr)
		data, callErr := d.call(ctx, channelAPIUser, "PcWebLogin", &wapi.LoginParams{
			Phone:        phone,
			Password:     password,
			UUID:         uuidStr,
			VerifyCode:   "",
			ClientSecret: clientSecret,
		})
		return handleLoginResult(m, apiHost, phone, uuidStr, "", data, callErr)
	case "captcha":
		code := configIn.Result
		phone, _ := m.Get("config_phone")
		encPassword, _ := m.Get("config_password")
		uuidStr, _ := m.Get("config_uuid")
		if phone == "" || encPassword == "" || uuidStr == "" {
			return nil, errors.New(`wocloud: interactive login state was lost - run "rclone config reconnect <remote>:" in a terminal, or supply access_token`)
		}
		if code == "" {
			return captchaPrompt(apiHost, uuidStr, "The captcha can't be blank")
		}
		password, err := obscure.Reveal(encPassword)
		if err != nil {
			return nil, fmt.Errorf("wocloud: failed to reveal password: %w", err)
		}
		m.Set("config_verify_code", code)
		data, callErr := d.call(ctx, channelAPIUser, "PcWebLogin", &wapi.LoginParams{
			Phone:        phone,
			Password:     password,
			UUID:         uuidStr,
			VerifyCode:   code,
			ClientSecret: clientSecret,
		})
		return handleLoginResult(m, apiHost, phone, uuidStr, code, data, callErr)
	case "sms":
		code := configIn.Result
		phone, _ := m.Get("config_phone")
		encPassword, _ := m.Get("config_password")
		uuidStr, _ := m.Get("config_uuid")
		verifyCode, _ := m.Get("config_verify_code")
		if phone == "" || encPassword == "" || uuidStr == "" {
			return nil, errors.New(`wocloud: interactive login state was lost - run "rclone config reconnect <remote>:" in a terminal, or supply access_token`)
		}
		if code == "" {
			return smsPrompt(phone, "The SMS code can't be blank")
		}
		password, err := obscure.Reveal(encPassword)
		if err != nil {
			return nil, fmt.Errorf("wocloud: failed to reveal password: %w", err)
		}
		data, callErr := d.call(ctx, channelAPIUser, "PcLoginVerifyCode", &wapi.VerifyParams{
			Phone:        phone,
			Password:     password,
			UUID:         uuidStr,
			VerifyCode:   verifyCode,
			MessageCode:  code,
			ClientSecret: clientSecret,
		})
		return handleLoginResult(m, apiHost, phone, uuidStr, verifyCode, data, callErr)
	case "retry":
		if configIn.Result == "true" {
			return fs.ConfigGoto("login")
		}
		return nil, errors.New("wocloud: login cancelled")
	}
	return nil, fmt.Errorf("wocloud: unknown config state %q", configIn.State)
}

// handleLoginResult interprets a login response or error
func handleLoginResult(m configmap.Mapper, apiHost, phone, uuidStr, verifyCode string, data json.RawMessage, callErr error) (*fs.ConfigOut, error) {
	if callErr != nil {
		var apiErr *apiError
		if errors.As(callErr, &apiErr) {
			switch apiErr.Code {
			case "6006":
				return captchaPrompt(apiHost, uuidStr, "")
			case "6007":
				return captchaPrompt(apiHost, uuidStr, "The captcha was incorrect")
			case "6008":
				return smsPrompt(phone, "")
			case "8001", "1001":
				return configRetry("Login failed: " + apiErr.Desc)
			}
		}
		return nil, callErr
	}

	var login wapi.LoginData
	if len(data) > 0 && string(data) != "null" {
		if err := json.Unmarshal(data, &login); err != nil {
			return nil, fmt.Errorf("wocloud: failed to parse login response: %w", err)
		}
	}
	if login.AccessToken != "" {
		m.Set("access_token", login.AccessToken)
		m.Set("phone", phone)
		m.Set("config_password", "")
		return nil, nil
	}
	if isTrue(login.NeedSmsCode) {
		return smsPrompt(phone, "")
	}
	if login.IsNeedVerfyCode == 1 || login.IsNeedVerifyCode == 1 {
		if verifyCode == "" {
			return captchaPrompt(apiHost, uuidStr, "")
		}
		return smsPrompt(phone, "")
	}
	return configRetry("Login succeeded but no access token was returned")
}

// captchaPrompt asks for the graphical captcha
func captchaPrompt(apiHost, uuidStr, errMsg string) (*fs.ConfigOut, error) {
	out, err := fs.ConfigInput("captcha", "config_captcha", fmt.Sprintf(
		"Open %s/api-user/getverifycode?uuid=%s in a browser to view the captcha, then enter it here.",
		apiHost, uuidStr))
	if out != nil {
		out.Error = errMsg
	}
	return out, err
}

// smsPrompt asks for the SMS code
func smsPrompt(phone, errMsg string) (*fs.ConfigOut, error) {
	out, err := fs.ConfigInput("sms", "config_sms", fmt.Sprintf(
		"Enter the SMS code sent to %s.", phone))
	if out != nil {
		out.Error = errMsg
	}
	return out, err
}

// configRetry asks whether to retry the login
func configRetry(msg string) (*fs.ConfigOut, error) {
	return fs.ConfigConfirm("retry", true, "config_retry", msg+"\n\nTry again?")
}

// isTrue returns true for the "1"/"true" values used by the API
func isTrue(s wapi.String) bool {
	return string(s) == "1" || strings.EqualFold(string(s), "true")
}

// Check the interfaces are satisfied
var (
	_ fs.Fs         = (*Fs)(nil)
	_ fs.Purger     = (*Fs)(nil)
	_ fs.Object     = (*Object)(nil)
	_ fs.IDer       = (*Object)(nil)
	_ fs.ParentIDer = (*Object)(nil)
)
