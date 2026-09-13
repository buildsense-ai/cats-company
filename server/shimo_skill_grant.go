package server

import (
	"database/sql"
	"errors"
	"log"
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

const (
	shimoGrantSkipSameActor      = "same_actor_recipient"
	shimoGrantSkipRecipientHuman = "recipient_not_bot"
)

// buildShimoSkillConnectorMetadata creates a per-message capability for the
// one operator-approved SkillHub package. The value exists only on live bot
// delivery metadata; stored messages and history replay never contain it.
func (h *Hub) buildShimoSkillConnectorMetadata(actorUID, recipientUID int64, topicID string, msgID int64) map[string]interface{} {
	if h == nil {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, "hub_nil", "")
	}
	if h.db == nil {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, "db_nil", "")
	}
	if actorUID <= 0 || recipientUID <= 0 {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, "invalid_identity", "")
	}
	if actorUID == recipientUID {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, shimoGrantSkipSameActor, "")
	}
	if msgID <= 0 {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, "missing_message_id", "")
	}
	if !h.isBotUser(recipientUID) {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, shimoGrantSkipRecipientHuman, "")
	}
	actor, err := h.db.GetUser(actorUID)
	if err != nil {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, "actor_lookup_error", shimoErrorCategory(err))
	}
	if actor == nil {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, "actor_missing", "")
	}
	if actor.AccountType == types.AccountBot || actor.AccountType == types.AccountService {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, "actor_not_human", "")
	}
	secret := []byte(strings.TrimSpace(os.Getenv("CATSCO_SHIMO_ACTOR_SECRET")))
	skillID := strings.TrimSpace(os.Getenv("CATSCO_SHIMO_SKILL_ID"))
	connectorURL := normalizeShimoConnectorURL(os.Getenv("CATSCO_SHIMO_CONNECTOR_URL"))
	if len(secret) < 32 {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, "invalid_secret", "")
	}
	if !shimoSkillIDPattern.MatchString(skillID) {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, "invalid_skill_id", "")
	}
	if connectorURL == "" {
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, "invalid_connector_url", "")
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
		return logShimoGrantSkip(actorUID, recipientUID, topicID, msgID, "token_generation_error", shimoErrorCategory(err))
	}
	logShimoGrantIssued(actorUID, recipientUID, topicID, msgID)
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

func logShimoGrantIssued(actorUID, recipientUID int64, topicID string, msgID int64) {
	log.Printf("[shimo_connector] grant issued: topic=%s actor=%s recipient=%s msg_id=%d", topicID, formatUID(actorUID), formatUID(recipientUID), msgID)
}

// logShimoGrantSkip records only a reason and message identity. It deliberately
// omits connector URLs, Skill IDs, and all credential material. The two high
// volume non-bot reasons require the explicit debug switch; configured grant
// failures are logged so operators can diagnose the authorized delivery path.
func logShimoGrantSkip(actorUID, recipientUID int64, topicID string, msgID int64, reason, category string) map[string]interface{} {
	shouldLog := reason != shimoGrantSkipSameActor && reason != shimoGrantSkipRecipientHuman
	if !shouldLog {
		shouldLog = shimoGrantDiagnosticsEnabled()
	}
	if shouldLog && (strings.TrimSpace(os.Getenv("CATSCO_SHIMO_ACTOR_SECRET")) != "" ||
		strings.TrimSpace(os.Getenv("CATSCO_SHIMO_SKILL_ID")) != "" ||
		strings.TrimSpace(os.Getenv("CATSCO_SHIMO_CONNECTOR_URL")) != "") {
		logShimoGrantSkipLine(actorUID, recipientUID, topicID, msgID, reason, category)
	}
	return nil
}

func logShimoGrantSkipLine(actorUID, recipientUID int64, topicID string, msgID int64, reason, category string) {
	if category != "" {
		log.Printf("[shimo_connector] grant skipped: reason=%s category=%s topic=%s actor=%s recipient=%s msg_id=%d", reason, category, topicID, formatUID(actorUID), formatUID(recipientUID), msgID)
		return
	}
	log.Printf("[shimo_connector] grant skipped: reason=%s topic=%s actor=%s recipient=%s msg_id=%d", reason, topicID, formatUID(actorUID), formatUID(recipientUID), msgID)
}

func shimoGrantDiagnosticsEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("CATSCO_SHIMO_GRANT_DEBUG")))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func shimoErrorCategory(err error) string {
	if errors.Is(err, sql.ErrNoRows) {
		return "not_found"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "required") || strings.Contains(message, "secret") || strings.Contains(message, "ttl") {
		return "validation"
	}
	if strings.Contains(message, "random") || strings.Contains(message, "entropy") {
		return "entropy"
	}
	if strings.Contains(message, "sign") || strings.Contains(message, "key") {
		return "signing"
	}
	return "other"
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
