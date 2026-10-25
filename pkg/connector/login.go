package connector

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/status"

	"matrix-fchat/pkg/fchat"
)

const (
	LoginFlowIDPassword = "password"

	loginStepCredentials = "fi.mau.fchat.login.credentials"
	loginStepCharacter   = "fi.mau.fchat.login.character"
	loginStepComplete    = "fi.mau.fchat.login.complete"
)

func (fc *FChatConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "Password",
		Description: "Log in with your F-List account name and password, then choose a character",
		ID:          LoginFlowIDPassword,
	}}
}

func (fc *FChatConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != LoginFlowIDPassword {
		return nil, bridgev2.ErrInvalidLoginFlowID
	}
	return &PasswordLogin{Main: fc, User: user}, nil
}

type PasswordLogin struct {
	Main *FChatConnector
	User *bridgev2.User

	account  string
	password string
	ticket   *fchat.TicketResponse
}

var _ bridgev2.LoginProcessUserInput = (*PasswordLogin)(nil)

func (pl *PasswordLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       loginStepCredentials,
		Instructions: "Enter your F-List account name and password. The password is stored by the bridge, because F-Chat requires a new login ticket for every connection.",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:        bridgev2.LoginInputFieldTypeUsername,
				ID:          "account",
				Name:        "Account name",
				Description: "Your F-List account name (not a character name)",
			}, {
				Type:        bridgev2.LoginInputFieldTypePassword,
				ID:          "password",
				Name:        "Password",
				Description: "Your F-List account password",
			}},
		},
	}, nil
}

func (pl *PasswordLogin) Cancel() {}

func (pl *PasswordLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	if pl.ticket == nil {
		return pl.submitCredentials(ctx, input)
	}
	return pl.submitCharacter(ctx, input)
}

func (pl *PasswordLogin) submitCredentials(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	pl.account = strings.TrimSpace(input["account"])
	pl.password = input["password"]
	if pl.account == "" || pl.password == "" {
		return nil, errors.New("account name and password are required")
	}
	ticket, err := pl.Main.Tickets.Get(ctx, pl.account, pl.password, true)
	if err != nil {
		if errors.Is(err, fchat.ErrInvalidLogin) {
			return nil, errors.New("F-List rejected the account name or password")
		}
		return nil, fmt.Errorf("failed to get F-List API ticket: %w", err)
	}
	if len(ticket.Characters) == 0 {
		return nil, errors.New("this F-List account has no characters")
	}
	pl.ticket = ticket
	if len(ticket.Characters) == 1 {
		return pl.finish(ctx, ticket.Characters[0])
	}
	characters := slices.Clone(ticket.Characters)
	slices.SortFunc(characters, func(a, b string) int {
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	})
	defaultChar := ticket.DefaultCharacter
	if !slices.Contains(characters, defaultChar) {
		defaultChar = characters[0]
	}
	return &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeUserInput,
		StepID: loginStepCharacter,
		Instructions: fmt.Sprintf(
			"Choose the character to log in with. Available characters: %s\n\n"+
				"To use several characters, log in once per character.",
			strings.Join(characters, ", "),
		),
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:         bridgev2.LoginInputFieldTypeSelect,
				ID:           "character",
				Name:         "Character",
				Description:  "The character to connect to F-Chat with",
				DefaultValue: defaultChar,
				Options:      characters,
				Validate: func(s string) (string, error) {
					s = strings.TrimSpace(s)
					for _, char := range characters {
						if strings.EqualFold(char, s) {
							return char, nil
						}
					}
					return "", fmt.Errorf("%q is not a character of this account", s)
				},
			}},
		},
	}, nil
}

func (pl *PasswordLogin) submitCharacter(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	wanted := strings.TrimSpace(input["character"])
	for _, char := range pl.ticket.Characters {
		if strings.EqualFold(char, wanted) {
			return pl.finish(ctx, char)
		}
	}
	return nil, fmt.Errorf("%q is not a character of this account", wanted)
}

func (pl *PasswordLogin) finish(ctx context.Context, character string) (*bridgev2.LoginStep, error) {
	loginID := makeUserLoginID(character)
	existing := pl.Main.Bridge.GetCachedUserLoginByID(loginID)
	var channels map[string]string
	if existing != nil {
		if existing.UserMXID != pl.User.MXID {
			return nil, fmt.Errorf("%s is already logged in by another Matrix user", character)
		}
		// Keep the joined channels when logging in again.
		channels = existing.Metadata.(*UserLoginMetadata).Channels
		existing.Client.Disconnect()
	}
	ul, err := pl.User.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		RemoteName: character,
		RemoteProfile: status.RemoteProfile{
			Name: character,
		},
		Metadata: &UserLoginMetadata{
			Account:   pl.account,
			Password:  pl.password,
			Character: character,
			Channels:  channels,
		},
	}, &bridgev2.NewLoginParams{
		// Always create a fresh client so the new credentials are used.
		LoadUserLogin: func(ctx context.Context, login *bridgev2.UserLogin) error {
			login.Client = pl.Main.newClient(login)
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to save login: %w", err)
	}
	client := ul.Client.(*FChatClient)
	if err = client.waitForFirstConnect(ctx); err != nil {
		if existing == nil {
			ul.Delete(ctx, status.BridgeState{StateEvent: status.StateLoggedOut, Reason: "LOGIN_FAILED"}, bridgev2.DeleteOpts{DontCleanupRooms: true})
		}
		return nil, fmt.Errorf("failed to connect to F-Chat: %w", err)
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       loginStepComplete,
		Instructions: fmt.Sprintf("Successfully logged in as %s. Use `join <channel>` to join F-Chat channels.", character),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}
