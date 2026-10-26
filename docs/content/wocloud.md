---
title: "China Unicom WoCloud"
description: "Rclone docs for China Unicom WoCloud"
versionIntroduced: "v1.77"
---

# China Unicom WoCloud

[联通云盘 (China Unicom WoCloud)](https://pan.wo.cn) is the cloud
storage service operated by China Unicom.

This backend uses the same dispatcher API as the official web client.
It is an unofficial integration: the API is not documented publicly and
may change without notice.

## Configuration

The backend needs an access token. It can be obtained with the
interactive login built into `rclone config`, or copied from a client
which already has one.

Here is an example of how to make a remote called `remote`. First run:

```console
rclone config
```

This will guide you through an interactive setup process:

```text
No remotes found, make a new one?
n) New remote
s) Set configuration password
q) Quit config
n/s/q> n

Enter name for new remote.
name> remote

Option Storage.
Type of storage to configure.
Choose a number from below, or type in your own value.
XX / China Unicom WoCloud
   \ (wocloud)
Storage> wocloud

Option access_token.
Access token used for the WoCloud API.

Leave this blank to log in with a phone number and SMS code when rclone
configures the remote. You can also paste a token obtained from the
official web client here.

The token is sensitive: it grants full access to the account.
Enter a value. Press Enter to leave empty.
access_token> 

Edit advanced config?
y) Yes
n) No (default)
y/n> n

Option config_phone.
Phone number of the WoCloud account
Enter a value.
config_phone> 13800000000

Option config_password.
WoCloud password (only used for this login and not stored)
y) Yes, type in my own password
g) Generate random password
n) No, leave this optional password blank
y/g/n> y
Enter the password:
password:
Confirm the password:
password:

Option config_captcha.
Open https://panservice.mail.wo.cn/api-user/getverifycode?uuid=... in a browser to view the captcha, then enter it here.
Enter a value.
config_captcha> abcd

Option config_sms.
Enter the SMS code sent to 13800000000.
Enter a value.
config_sms> 123456

Configuration complete.
Options:
- type: wocloud
- access_token: ...
- phone: 13800000000
Keep this "remote" remote?
y) Yes this is OK (default)
e) Edit this remote
d) Delete this remote
y/e/d> y
```

Once configured you can then use `rclone` like this (replace `remote`
with the name you gave your remote):

List directories and files in the top level of your WoCloud

```console
rclone lsf remote:
```

To copy a local directory to a WoCloud directory called backup

```console
rclone copy /home/source remote:backup
```

### Access token

If you already have an access token then you can paste it into the
`access_token` option. This is the only option needed:

```ini
[remote]
type = wocloud
access_token = YOUR_ACCESS_TOKEN
```

This is the recommended way to configure the backend from the rc API,
the Web GUI or any other non-interactive environment.

### Re-authenticating

The interactive login needs a graphical captcha and an SMS code. The
multi-step login only works from the terminal. To refresh an expired
token run:

```console
rclone config reconnect remote:
```

The access token is sensitive and is marked as such, so
`rclone config redacted` will hide it.

### Spaces

`space_type` selects the cloud space:

| Value | Space |
|-------|-------|
| `0` | Personal cloud (default) |
| `1` | Family cloud - also set `family_id` |
| `4` | Private space - also set `ps_token` |

Obtaining a private space token, or joining a family cloud, is outside
the scope of this backend.

### Root folder

You can restrict rclone to a particular directory by setting
`root_folder_id`. The default is `0`, the root of the cloud disk.

Use `rclone lsf -Fip --dirs-only remote:` to find directory IDs.

## Limitations

### Empty files

The upload endpoint rejects zero length files and there is no API to
create an empty file. Uploading a file of size zero returns
`can't upload empty files to this remote`, and `rclone touch` will fail.

### Modification times and hashes

WoCloud returns a modification time in listings and rclone displays it,
but there is no API to set it. Advertising a real precision would make
`rclone sync` re-upload every file on every run, so the backend reports
that modification times are not supported. Sync therefore compares size
and, when asked, the MD5 hash.

The download endpoint returns the MD5 of the file in its `ETag` header.
rclone exposes this as an MD5 hash, but it costs an extra request per
file, so hash operations are slow.

Use `--checksum` when copying files which may have changed without their
size changing:

```console
rclone copy --checksum /home/source remote:backup
rclone check --checksum /home/source remote:backup
```

### Asynchronous changes

Deletes and moves are applied asynchronously. rclone waits for the
change to appear in the listing before continuing. The wait is
controlled by `--wocloud-poll-timeout` (default `30s`). Set it to `0`
to disable waiting if you are doing a large batch of changes and prefer
speed over immediate consistency.

### Directory creation limit

The service returns a "system exception" (code `9999`) after about 30
directories have been created in a short period. rclone creates
directories at a paced rate (about 24 per minute) to avoid this. Syncing
a tree with many new directories will therefore be slow; creating files
in directories which already exist is not affected. Running multiple
rclone processes against the same account at the same time can still hit
the limit, in which case wait a minute and retry.

### Uploads

Files are uploaded in a single request. This has been tested with files
up to 100 MiB. There is no chunked upload support, so very large files
may fail.

### Server-side copy, move and dir move

The API can move and copy files and directories but the responses do not
return the ID of the new object and copying renames on collision. The
backend does not implement server-side copy, move or dir move, so rclone
falls back to downloading and re-uploading. This is correct but slower.

### About, link and recycle bin

Quota information is not exposed by the API used here, so `rclone about`
is not supported. `rclone link` is not supported. Files are moved to the
recycle bin when deleted; emptying the recycle bin is not supported.

### Filename restrictions

The service rejects `*`, `?`, `<` and `>` in file names with an HTTP 500
error. These characters, and the other characters which are invalid on
Windows, are encoded by default. Some non-ASCII symbols, including
emoji and characters outside the Basic Multilingual Plane, are also
rejected by the service and cannot be stored.

### Interactive login

The interactive login only works in a terminal because it needs to hold
the password and the login state between the captcha and SMS steps. In
the rc API or Web GUI, set `access_token` instead.

<!-- autogenerated options start - DO NOT EDIT - instead edit fs.RegInfo in backend/wocloud/wocloud.go and run make backenddocs to verify --> <!-- markdownlint-disable-line line-length -->
### Standard options

Here are the Standard options specific to wocloud (China Unicom WoCloud).

#### --wocloud-access-token

Access token used for the WoCloud API.

Leave this blank to log in with a phone number and SMS code when rclone
configures the remote. You can also paste a token obtained from the
official web client here.

The token is sensitive: it grants full access to the account.

Properties:

- Config:      access_token
- Env Var:     RCLONE_WOCLOUD_ACCESS_TOKEN
- Type:        string
- Required:    false

### Advanced options

Here are the Advanced options specific to wocloud (China Unicom WoCloud).

#### --wocloud-phone

Phone number used to log in.

This is only used by the interactive login and is not required when an
access_token is supplied.

Properties:

- Config:      phone
- Env Var:     RCLONE_WOCLOUD_PHONE
- Type:        string
- Required:    false

#### --wocloud-space-type

Cloud space to use: 0 personal, 1 family, 4 private.

Properties:

- Config:      space_type
- Env Var:     RCLONE_WOCLOUD_SPACE_TYPE
- Type:        string
- Default:     "0"
- Choices:
  - "0"
    - Personal cloud
  - "1"
    - Family cloud (requires family_id)
  - "4"
    - Private space (requires ps_token)

#### --wocloud-family-id

Family ID for the family cloud.

Only used when space_type is 1.

Properties:

- Config:      family_id
- Env Var:     RCLONE_WOCLOUD_FAMILY_ID
- Type:        string
- Required:    false

#### --wocloud-ps-token

Private space token.

Only used when space_type is 4. Obtaining this token is outside the
scope of this backend.

Properties:

- Config:      ps_token
- Env Var:     RCLONE_WOCLOUD_PS_TOKEN
- Type:        string
- Required:    false

#### --wocloud-root-folder-id

ID of the root folder.

Leave this as "0" (the cloud disk root) unless you want to restrict
rclone to a particular directory.

Properties:

- Config:      root_folder_id
- Env Var:     RCLONE_WOCLOUD_ROOT_FOLDER_ID
- Type:        string
- Default:     "0"

#### --wocloud-api-host

Dispatcher host.

Normally this should be left alone. It is configurable so that the
backend can be tested against a local server.

Properties:

- Config:      api_host
- Env Var:     RCLONE_WOCLOUD_API_HOST
- Type:        string
- Default:     "https://panservice.mail.wo.cn"

#### --wocloud-upload-host

Upload host.

Leave blank to discover it with the GetZoneInfo operation. If discovery
fails the backend falls back to the known production host.

Properties:

- Config:      upload_host
- Env Var:     RCLONE_WOCLOUD_UPLOAD_HOST
- Type:        string
- Required:    false

#### --wocloud-client-id

Client ID used to sign dispatcher requests.

This is not user specific. It is configurable in case the service
changes the value.

Properties:

- Config:      client_id
- Env Var:     RCLONE_WOCLOUD_CLIENT_ID
- Type:        string
- Default:     "1001000021"

#### --wocloud-client-secret

Client secret used to encrypt dispatcher requests.

This is not user specific. It is configurable in case the service
changes the value.

Properties:

- Config:      client_secret
- Env Var:     RCLONE_WOCLOUD_CLIENT_SECRET
- Type:        string
- Default:     "XFmi9GS2hzk98jGX"

#### --wocloud-page-size

Number of entries to request in each listing call.

Properties:

- Config:      page_size
- Env Var:     RCLONE_WOCLOUD_PAGE_SIZE
- Type:        int
- Default:     100

#### --wocloud-poll-timeout

How long to wait for the listing to reflect a change.

WoCloud updates its listings asynchronously after deletes and moves.
rclone waits up to this long for the change to become visible so that
the next listing is consistent.

Set to 0 to disable waiting.

Properties:

- Config:      poll_timeout
- Env Var:     RCLONE_WOCLOUD_POLL_TIMEOUT
- Type:        Duration
- Default:     30s

#### --wocloud-encoding

The encoding for the backend.

See the [encoding section in the overview](/overview/#encoding) for more info.

Properties:

- Config:      encoding
- Env Var:     RCLONE_WOCLOUD_ENCODING
- Type:        Encoding
- Default:     Slash,LtGt,DoubleQuote,Colon,Question,Asterisk,Pipe,BackSlash,Del,Ctl,LeftSpace,LeftPeriod,RightSpace,RightPeriod,InvalidUtf8,Dot

#### --wocloud-description

Description of the remote.

Properties:

- Config:      description
- Env Var:     RCLONE_WOCLOUD_DESCRIPTION
- Type:        string
- Required:    false

<!-- autogenerated options stop -->
