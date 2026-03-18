package domain

type OwnerType string

const (
	OwnerGuest   OwnerType = "guest"
	OwnerAccount OwnerType = "account"
)

type AuthCredentialType string

const (
	AuthCredentialNone           AuthCredentialType = ""
	AuthCredentialGuestToken     AuthCredentialType = "guest_token"
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
