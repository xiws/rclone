// Package api has type definitions for the WoCloud (China Unicom cloud
// disk) dispatcher protocol.
//
// The protocol was reverse engineered from the pan.wo.cn web bundle and
// verified against a real account. See docs/liantong/ for the research
// notes (not part of the published documentation).
package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Response is the standard dispatcher response envelope.
type Response struct {
	Status string `json:"STATUS"`
	Msg    string `json:"MSG"`
	LogID  string `json:"LOGID"`
	RSP    RSP    `json:"RSP"`
}

// RSP is the result of a dispatcher operation.
type RSP struct {
	Code string          `json:"RSP_CODE"`
	Desc string          `json:"RSP_DESC"`
	Data json.RawMessage `json:"DATA"`
}

// Request is the outer dispatcher request.
type Request struct {
	Header Header `json:"header"`
	Body   Body   `json:"body"`
}

// Header identifies and signs a dispatcher call.
type Header struct {
	Key     string `json:"key"`
	ResTime int64  `json:"resTime"`
	ReqSeq  int    `json:"reqSeq"`
	Channel string `json:"channel"`
	Sign    string `json:"sign"`
	Version string `json:"version"`
}

// Body carries the encrypted operation parameters.
//
// ClientID and Secret are used for the api-user channel; Key is used for
// the wohome channel.
type Body struct {
	Param    string `json:"param"`
	ClientID string `json:"clientId,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	Key      bool   `json:"key,omitempty"`
}

// Int64 is an int64 which unmarshals from either a JSON number or a JSON
// string. The WoCloud API is inconsistent about the types it returns.
type Int64 int64

// UnmarshalJSON implements json.Unmarshaler.
func (i *Int64) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*i = 0
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		s = str
	}
	if s == "" {
		*i = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid integer %q: %w", s, err)
	}
	*i = Int64(v)
	return nil
}

// MarshalJSON implements json.Marshaler.
func (i Int64) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(int64(i), 10)), nil
}

// String is a string which unmarshals from either a JSON string or a JSON
// number/bool. The WoCloud API is inconsistent about the types it returns.
type String string

// UnmarshalJSON implements json.Unmarshaler.
func (s *String) UnmarshalJSON(data []byte) error {
	t := strings.TrimSpace(string(data))
	if t == "" || t == "null" {
		*s = ""
		return nil
	}
	if t[0] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		*s = String(str)
		return nil
	}
	*s = String(t)
	return nil
}

// Item types returned by the API.
const (
	TypeDirectory Int64 = 0
	TypeFile      Int64 = 1
)

// Item is a file or directory returned in a listing.
type Item struct {
	Fid               string `json:"fid"`
	ID                string `json:"id"`
	Name              string `json:"name"`
	Size              Int64  `json:"size"`
	Type              Int64  `json:"type"`
	FileType          String `json:"fileType"`
	FileVersion       Int64  `json:"fileVersion"`
	ParentDirectoryID string `json:"parentDirectoryId"`
	CreateTime        String `json:"createTime"`
	ModifyTime        String `json:"modifyTime"`
	SHA256            string `json:"sha256"`
	FileUniqueValue   string `json:"fileUniqueValue"`
	Creator           string `json:"creator"`
	SpaceType         String `json:"spaceType"`
	UnitID            string `json:"unitId"`
}

// Directory is a directory returned by QueryDirectorys.
type Directory struct {
	ID            string `json:"id"`
	DirectoryName string `json:"directoryName"`
	CreateTime    String `json:"createTime"`
	Creator       string `json:"creator"`
	AllowDelete   String `json:"allowDelete"`
}

// ListResponse is returned by QueryAllFiles and QueryDirectorys.
type ListResponse struct {
	Files       []Item      `json:"files"`
	SystemDirs  []Item      `json:"systemDirs"`
	Directorys  []Directory `json:"directorys"`
	HasNextPage String      `json:"hasNextPage"`
	Total       Int64       `json:"total"`
}

// CreateDirectoryData is returned by CreateDirectory.
type CreateDirectoryData struct {
	ID string `json:"id"`
}

// DownloadItem is returned by GetDownloadUrl.
type DownloadItem struct {
	Fid         string `json:"fid"`
	DownloadURL string `json:"downloadUrl"`
}

// ZoneInfo is returned by GetZoneInfo.
type ZoneInfo struct {
	URL string `json:"url"`
}

// UploadFileInfo is encrypted with the access token and sent as the
// fileInfo multipart field of upload2C.
type UploadFileInfo struct {
	FileName    string `json:"fileName"`
	FileSize    int64  `json:"fileSize"`
	FileType    string `json:"fileType"`
	DirectoryID string `json:"directoryId"`
	BatchNo     string `json:"batchNo"`
	SpaceType   string `json:"spaceType"`
}

// UploadResponse is returned by upload2C, which does not use the
// dispatcher envelope.
type UploadResponse struct {
	Code string     `json:"code"`
	Msg  string     `json:"msg"`
	Data UploadData `json:"data"`
}

// UploadData is the payload of a successful upload2C response.
type UploadData struct {
	Fid         string `json:"fid"`
	FileVersion String `json:"fileVersion"`
	WcFileID    string `json:"wcFileId"`
}

// LoginData is returned by PcWebLogin and PcLoginVerifyCode.
type LoginData struct {
	AccessToken      string `json:"access_token"`
	NeedSmsCode      String `json:"needSmsCode"`
	IsNeedVerfyCode  Int64  `json:"isNeedVerfyCode"`
	IsNeedVerifyCode Int64  `json:"isNeedVerifyCode"`
}

// UsageInfo holds the quota information embedded in AppQueryUser.
type UsageInfo struct {
	AllSpace  Int64 `json:"allSpace"`
	UsedSpace Int64 `json:"usedSpace"`
}

// UserData is returned by AppQueryUser.
type UserData struct {
	UserID    string    `json:"userId"`
	UserName  string    `json:"userName"`
	UsageInfo UsageInfo `json:"usageInfo"`
}

// SpaceParams are the parameters which select a cloud space.
type SpaceParams struct {
	SpaceType string `json:"spaceType"`
	FamilyID  string `json:"familyId,omitempty"`
	PsToken   string `json:"psToken,omitempty"`
}

// ListParams is the parameter set for QueryAllFiles/QueryTypeFiles.
type ListParams struct {
	SpaceParams
	ParentDirectoryID string `json:"parentDirectoryId"`
	PageNum           int    `json:"pageNum"`
	PageSize          int    `json:"pageSize"`
	SortRule          string `json:"sortRule"`
}

// CreateDirectoryParams is the parameter set for CreateDirectory.
type CreateDirectoryParams struct {
	IsCouldRepeat string `json:"isCouldRepeat"`
	SpaceParams
	ParentDirectoryID string `json:"parentDirectoryId"`
	DirectoryName     string `json:"directoryName"`
}

// RenameParams is the parameter set for RenameFileOrDirectory.
type RenameParams struct {
	SpaceParams
	Type     int    `json:"type"`
	FileType string `json:"fileType"`
	ID       string `json:"id"`
	Name     string `json:"name"`
}

// DeleteParams is the parameter set for DeleteFile.
type DeleteParams struct {
	SpaceParams
	VipLevel string   `json:"vipLevel"`
	DirList  []string `json:"dirList"`
	FileList []string `json:"fileList"`
}

// MoveParams is the parameter set for MoveFile/CopyFile.
type MoveParams struct {
	TargetDirID string   `json:"targetDirId"`
	SourceType  int      `json:"sourceType"`
	TargetType  int      `json:"targetType"`
	DirList     []string `json:"dirList"`
	FileList    []string `json:"fileList"`
}

// DownloadParams is the parameter set for GetDownloadUrl.
type DownloadParams struct {
	FidList   []string `json:"fidList"`
	ClientID  string   `json:"clientId"`
	SpaceType string   `json:"spaceType"`
}

// ZoneInfoParams is the parameter set for GetZoneInfo.
type ZoneInfoParams struct {
	AppID string `json:"appId"`
}

// LoginParams is the parameter set for PcWebLogin.
type LoginParams struct {
	Phone        string `json:"phone"`
	Password     string `json:"password"`
	UUID         string `json:"uuid"`
	VerifyCode   string `json:"verifyCode"`
	ClientSecret string `json:"clientSecret"`
}

// VerifyParams is the parameter set for PcLoginVerifyCode.
type VerifyParams struct {
	Phone        string `json:"phone"`
	Password     string `json:"password"`
	UUID         string `json:"uuid"`
	VerifyCode   string `json:"verifyCode"`
	MessageCode  string `json:"messageCode"`
	ClientSecret string `json:"clientSecret"`
}

// QueryUserParams is the parameter set for AppQueryUser.
type QueryUserParams struct {
	AccessToken string `json:"accessToken"`
}
