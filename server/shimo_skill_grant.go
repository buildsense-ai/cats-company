package server

import (
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const shimoSkillGrantTTL = 10 * time.Minute

var shimoSkillIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// buildShimoSkillConnectorMetadata creates a per-message capability for the
// one operator-approved SkillHub package. The value exists only on live bot
// delivery metadata; stored messages and history replay never contain it.
func (h *Hub) buildShimoSkillConnectorMetadata(actorUID, recipientUID int64, topicID string, msgID int64) map[string]interface{} {
	if h == nil || h.db == nil || actorUID <= 0 || recipientUID <= 0 || actorUID == recipientUID || msgID <= 0 || !h.isBotUser(recipientUID) {
		return nil
	}
	actor, err := h.db.GetUser(actorUID)
	if err != nil || actor == nil || actor.AccountType == types.AccountBot || actor.AccountType == types.AccountService {
		return nil
	}
	secret := []byte(strings.TrimSpace(os.Getenv("CATSCO_SHIMO_ACTOR_SECRET")))
	skillID := strings.TrimSpace(os.Getenv("CATSCO_SHIMO_SKILL_ID"))
	connectorURL := normalizeShimoConnectorURL(os.Getenv("CATSCO_SHIMO_CONNECTOR_URL"))
	if len(secret) < 32 || !shimoSkillIDPattern.MatchString(skillID) || connectorURL == "" {
		return nil
	}

	now := time.Now().UTC()
	taskRef := "catsco:" + strings.TrimSpace(topicID) + ":" + strconv.FormatInt(msgID, 10)
	token, err := GenerateShimoActorToken(
		secret,
		formatUID(recipientUID),
		formatUID(actorUID),
		taskRef,
		skillID,
		shimoSkillGrantTTL,
	)
	if err != nil {
		return nil
	}
	return map[string]interface{}{
		"schema": "catsco.skill_connectors.v1",
		"grants": []map[string]interface{}{
			{
				"provider":      "shimo",
				"skill_id":      skillID,
				"connector_url": connectorURL,
				"actor_token":   token,
				"expires_at":    now.Add(shimoSkillGrantTTL).Format(time.RFC3339),
			},
		},
	}
}

func normalizeShimoConnectorURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1")) {
		return ""
	}
	if host == "" {
		return ""
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/")
}
