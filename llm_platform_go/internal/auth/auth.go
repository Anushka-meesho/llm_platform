package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type contextKey string

const userContextKey contextKey = "auth_user"

// User represents an authenticated UI user.
type User struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name,omitempty"`
	Issuer  string `json:"iss,omitempty"`
}

type Claims struct {
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
	jwt.RegisteredClaims
}

func NewClaims(user *User, issuer string, expiry time.Duration) *Claims {
	now := time.Now().UTC()
	return &Claims{
		Email: user.Email,
		Name:  user.Name,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.Subject,
			Issuer:    issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
		},
	}
}

func IssueToken(user *User, secret []byte, issuer string, expiry time.Duration) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, NewClaims(user, issuer, expiry))
	return token.SignedString(secret)
}

func ParseToken(tokenString string, secret []byte) (*User, error) {
	if tokenString == "" {
		return nil, errors.New("missing token")
	}

	claims := &Claims{}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	token, err := parser.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	if claims.Subject == "" || claims.Email == "" {
		return nil, errors.New("missing required claims")
	}

	return &User{
		Subject: claims.Subject,
		Email:   claims.Email,
		Name:    claims.Name,
		Issuer:  claims.Issuer,
	}, nil
}

func TokenFromRequest(r *http.Request, cookieName string) (string, error) {
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			return strings.TrimSpace(authHeader[len("bearer "):]), nil
		}
		return "", errors.New("authorization header must use Bearer")
	}

	cookie, err := r.Cookie(cookieName)
	if err == nil {
		return cookie.Value, nil
	}
	return "", errors.New("token not found")
}

func WithUser(ctx context.Context, user *User) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

func FromContext(ctx context.Context) (*User, bool) {
	user, ok := ctx.Value(userContextKey).(*User)
	return user, ok
}

func SetAuthCookie(w http.ResponseWriter, cookieName, token, domain string, secure bool, maxAge int) {
	cookie := &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
	if domain != "" {
		cookie.Domain = domain
	}
	http.SetCookie(w, cookie)
}

func ClearAuthCookie(w http.ResponseWriter, cookieName, domain string) {
	cookie := &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
	if domain != "" {
		cookie.Domain = domain
	}
	http.SetCookie(w, cookie)
}
