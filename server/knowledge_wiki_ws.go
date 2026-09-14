package server

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const knowledgeWikiWebSocketTokenType = "knowledge_wiki_ws"

type knowledgeWikiWebSocketClaims struct {
	TokenType string `json:"token_type"`
	ViewerUID int64  `json:"viewer_uid"`
	AgentUID  int64  `json:"agent_uid"`
	jwt.RegisteredClaims
}

func GenerateKnowledgeWikiWebSocketToken(viewerUID, agentUID int64) (string, error) {
	if viewerUID <= 0 || agentUID <= 0 {
		return "", fmt.Errorf("invalid wiki websocket scope")
	}
	claims := knowledgeWikiWebSocketClaims{TokenType: knowledgeWikiWebSocketTokenType, ViewerUID: viewerUID, AgentUID: agentUID,
		RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)), Issuer: "catscompany", Audience: []string{"catsco-wiki"}}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtSecret)
}

func ParseKnowledgeWikiWebSocketToken(value string) (*knowledgeWikiWebSocketClaims, error) {
	claims := &knowledgeWikiWebSocketClaims{}
	token, err := jwt.ParseWithClaims(value, claims, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return jwtSecret, nil
	}, jwt.WithIssuer("catscompany"), jwt.WithAudience("catsco-wiki"))
	if err != nil || token == nil || !token.Valid || claims.TokenType != knowledgeWikiWebSocketTokenType || claims.ViewerUID <= 0 || claims.AgentUID <= 0 {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("invalid wiki websocket token")
	}
	return claims, nil
}
