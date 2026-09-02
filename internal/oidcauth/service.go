package oidcauth

import (
	"context"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const ProviderID = "default"

type Config struct {
	Name         string
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

type Service struct {
	config   Config
	oauth2   oauth2.Config
	verifier *oidc.IDTokenVerifier
}

type Claims struct {
	Subject string `json:"sub"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Nonce   string `json:"nonce"`
}

func New(ctx context.Context, config Config) (*Service, error) {
	provider, err := oidc.NewProvider(ctx, config.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	return &Service{
		config: config,
		oauth2: oauth2.Config{
			ClientID:     config.ClientID,
			ClientSecret: config.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  config.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: config.ClientID}),
	}, nil
}

func (s *Service) Name() string     { return s.config.Name }
func (s *Service) Issuer() string   { return s.config.Issuer }
func (s *Service) ClientID() string { return s.config.ClientID }

func (s *Service) AuthorizationURL(state, nonce, pkceVerifier string) string {
	return s.oauth2.AuthCodeURL(
		state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(pkceVerifier),
	)
}

func (s *Service) Exchange(ctx context.Context, code, pkceVerifier, expectedNonce string) (Claims, error) {
	token, err := s.oauth2.Exchange(ctx, code, oauth2.VerifierOption(pkceVerifier))
	if err != nil {
		return Claims{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Claims{}, fmt.Errorf("OIDC response did not contain an ID token")
	}
	idToken, err := s.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return Claims{}, fmt.Errorf("verify ID token: %w", err)
	}
	var claims Claims
	if err := idToken.Claims(&claims); err != nil {
		return Claims{}, fmt.Errorf("decode ID token claims: %w", err)
	}
	if claims.Subject == "" {
		return Claims{}, fmt.Errorf("ID token subject is empty")
	}
	if claims.Nonce != expectedNonce {
		return Claims{}, fmt.Errorf("ID token nonce does not match")
	}
	return claims, nil
}
