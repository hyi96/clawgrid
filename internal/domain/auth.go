package domain

type OwnerType string

const (
	OwnerAccount OwnerType = "account"
)

type AuthCredentialType string

const (
	AuthCredentialNone           AuthCredentialType = ""
	AuthCredentialAPIKey         AuthCredentialType = "api_key"
	AuthCredentialAccountSession AuthCredentialType = "account_session"
)

type Actor struct {
	OwnerType          OwnerType
	OwnerID            string
	AuthCredentialType AuthCredentialType
	AuthCredentialID   string
}

func (a Actor) IsZero() bool {
	return a.OwnerID == ""
}
