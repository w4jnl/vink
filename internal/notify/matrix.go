package notify

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// MatrixConfig posts to a room with an access token of a bot user.
type MatrixConfig struct {
	Homeserver  string `json:"homeserver"`
	AccessToken string `json:"access_token"`
	RoomID      string `json:"room_id"`
}

// Matrix sends an m.text message with a formatted body.
type Matrix struct {
	client *http.Client
	now    func() time.Time
}

func (m *Matrix) Kind() domain.ChannelKind { return domain.ChannelMatrix }

func parseMatrix(cfg json.RawMessage) (MatrixConfig, error) {
	var c MatrixConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, validationError(err.Error())
	}
	u, err := url.Parse(strings.TrimSpace(c.Homeserver))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return c, validationError("homeserver must be an absolute http(s) URL such as https://matrix.example.com")
	}
	c.Homeserver = strings.TrimRight(u.String(), "/")
	c.AccessToken = strings.TrimSpace(c.AccessToken)
	if c.AccessToken == "" {
		return c, validationError("access_token must be set")
	}
	c.RoomID = strings.TrimSpace(c.RoomID)
	if !strings.HasPrefix(c.RoomID, "!") || !strings.Contains(c.RoomID, ":") {
		return c, validationError("room_id must be a room id such as !abc:example.com")
	}
	return c, nil
}

func (m *Matrix) Validate(cfg json.RawMessage) error {
	_, err := parseMatrix(cfg)
	return err
}

func (m *Matrix) Send(ctx context.Context, cfg json.RawMessage, n Notification) error {
	c, err := parseMatrix(cfg)
	if err != nil {
		return err
	}
	now := time.Now
	if m.now != nil {
		now = m.now
	}
	txn := "vink-" + strconv.FormatInt(now().UnixNano(), 36)
	endpoint := c.Homeserver + "/_matrix/client/v3/rooms/" + url.PathEscape(c.RoomID) + "/send/m.room.message/" + txn
	text := n.Title() + "\n" + n.Text()
	formatted := "<b>" + html.EscapeString(n.Title()) + "</b><br>" + strings.ReplaceAll(html.EscapeString(n.Text()), "\n", "<br>")
	body := map[string]any{"msgtype": "m.text", "body": text, "format": "org.matrix.custom.html", "formatted_body": formatted}
	return sendJSON(ctx, m.client, http.MethodPut, endpoint, map[string]string{"Authorization": "Bearer " + c.AccessToken}, body, "matrix")
}

var _ Notifier = (*Matrix)(nil)
