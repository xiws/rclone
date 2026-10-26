# WoCloud API observations

These notes record the responses observed while implementing the backend.
They are not part of the user documentation and contain no credentials.

## Dispatcher

- All operations are `POST {api_host}/{api-user|wohome}/dispatcher`.
- `header.sign = md5(key + resTime + reqSeq + channel + version)`.
- `wohome` requests encrypt the parameters with the access token and
  inject `clientId`; responses are encrypted with the access token.
- `api-user` requests encrypt with the client secret; `AppQueryUser`
  responses are also encrypted with the client secret.
- Invalid token returns `RSP_CODE 1001`.
- `9999` (system exception) has been seen transiently, so it is retried.

## Listing

`QueryAllFiles` returns:

```json
{
  "files": [
    {"fid": "", "id": "...", "name": "dir", "size": 0, "type": 0,
     "fileType": "", "fileVersion": "", "parentDirectoryId": "0",
     "createTime": "20261025095616", "modifyTime": "20261025095616",
     "sha256": ""},
    {"fid": "...", "id": "...", "name": "a.txt", "size": 12, "type": 1,
     "fileType": "4", "fileVersion": 1, "parentDirectoryId": "...",
     "createTime": "20261025110250", "modifyTime": "20261025110250",
     "sha256": ""}
  ],
  "systemDirs": []
}
```

- `type` 0 is a directory and 1 is a file.
- `systemDirs` is normally empty and is not a real directory listing.
- `size` is a JSON number. `fileVersion` is a number in listings and a
  string in the upload response. `fileType` is a string.
- `modifyTime`/`createTime` are `YYYYMMDDHHMMSS` in China Standard Time.
- `sha256` is empty for the files observed.
- `pageNum` starts at 0. `pageSize` of 300 worked.
- `QueryDirectorys` returns `directorys[]` with `id`, `directoryName`
  and `createTime`.

## Writes

- `upload2C` overwrites a file with the same name and keeps its `id` and
  `fid`; `fileVersion` is incremented. The response's `wcFileId` equals
  the listing `id` and `data.fid` equals the listing `fid`.
- Uploading a zero length file returns HTTP 400, so empty files are not
  supported.
- Single request uploads of 20 MiB and 100 MiB worked.
- `GetZoneInfo` returns `{"url": "https://hyupload.pan.wo.cn"}`.
- `DeleteFile` removes files and directories; deleting a directory is
  recursive. Deleting an unknown ID returns success. The listing can be
  briefly stale, so the backend waits for consistency.
- `CreateDirectory` returns `{"id": "..."}`. Creating a duplicate
  returns `130007`; the backend looks the existing directory up.
- Creating more than about 29 directories in a short period returns
  `9999` (system exception) until the limit resets. Uploads are not
  affected. The backend paces directory creation to stay below the limit.
- Uploading into a missing directory returns `130004`.
- Renaming onto an existing name returns `130012` (no overwrite).
- `MoveFile`/`CopyFile` take `sourceType` and `targetType` as JSON
  integers (0 for the personal cloud), not strings. `CopyFile`
  auto-renames on collision and neither response returns the new ID, so
  server-side copy/move are not implemented.
- `GetDownloadUrl` requires the `fid` field, not `id`.

## Downloads

- The returned URL is served by `hydownload.pan.wo.cn` and needs the
  `accesstoken` header.
- Range requests return 206 with `Content-Range`, so `rclone mount` can
  seek.
- The response `ETag` is the MD5 of the file, which the backend exposes
  as its MD5 hash (via an extra HEAD request).

## User information

`AppQueryUser` returns the user profile but no `usageInfo` for the
account used during development, so `rclone about` is not implemented.

## File names

- Chinese, Latin and the punctuation `' " : # % & + , ; = |` were
  accepted.
- `*`, `?`, `<` and `>` produce HTTP 500 and must be encoded.
- Emoji and other non-BMP/symbol characters produce HTTP 500.
- Leading/trailing spaces and dots are accepted but are encoded for
  Windows compatibility.
