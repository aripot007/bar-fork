package api

import (
	"bar/autogen"
	"bar/internal/config"
	"bar/internal/models"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/color"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/patrickmn/go-cache"
	"github.com/sirupsen/logrus"
	"github.com/skip2/go-qrcode"
	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/oauth2"
	admin "google.golang.org/api/admin/directory/v1"
	"google.golang.org/api/option"
)

var qrCache = cache.New(5*time.Minute, 10*time.Minute)
var stateCache = cache.New(5*time.Minute, 10*time.Minute)
var redirectCache = cache.New(5*time.Minute, 10*time.Minute)

// (GET /account/qr)
func (s *Server) GetAccountQR(c echo.Context) error {
	// Get account from cookie
	logged := c.Get("userLogged").(bool)
	loggedOnBoard := c.Get("onBoardLogged").(bool)
	if !logged && !loggedOnBoard {
		return ErrorNotAuthenticated(c)
	}

	var accountID string
	var account *models.Account

	if logged {
		accountID = c.Get("userAccountID").(string)
		account = c.Get("userAccount").(*models.Account)
	}

	if loggedOnBoard {
		accountID = c.Get("onBoardAccountID").(string)
		account = c.Get("onBoardAccount").(*models.Account)
	}

	var params autogen.GetAccountQRJSONBody
	err := c.Bind(&params)
	if err != nil {
		return Error400(c)
	}

	if !account.VerifyPin(params.CardPin) {
		return ErrorAccNotFound(c)
	}

	b64, found := qrCache.Get(accountID)
	if !found {
		// Generate QR code nonce
		nonce := uuid.NewString()

		// Cache nonce
		qrCache.Set(nonce, accountID, cache.DefaultExpiration)

		conf := config.GetConfig()
		url := fmt.Sprintf("%s/auth/google/begin/%s", conf.ApiConfig.BasePath, nonce)
		qr, err := qrcode.New(url, qrcode.Medium)
		if err != nil {
			return Error500(c)
		}
		qr.BackgroundColor = color.RGBA{R: 255, G: 255, B: 255, A: 0}
		// Generate QR code
		png, err := qr.PNG(200)
		if err != nil {
			return Error500(c)
		}
		b64 = base64.StdEncoding.EncodeToString(png)
		qrCache.Set(accountID, b64, cache.DefaultExpiration)

		logrus.Debugf("QR code generated for account %s: %s", accountID, url)
	}

	// Convert to base64
	r := strings.NewReader(b64.(string))

	autogen.GetAccountQR200ImagepngResponse{
		ContentLength: int64(r.Len()),
		Body:          r,
	}.VisitGetAccountQRResponse(c.Response())
	return nil
}

// (GET /account/qr)
func (s *Server) GetAccountQRWebsocket(c echo.Context) error {
	logged := c.Get("userLogged").(bool)
	loggedOnBoard := c.Get("onBoardLogged").(bool)
	if !logged && !loggedOnBoard {
		return ErrorNotAuthenticated(c)
	}

	return Upgrade(c)
}

var scopes = []string{
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/admin.directory.user.readonly",
}

// (GET /auth/google/begin/{qr_nonce})
func (s *Server) ConnectAccount(c echo.Context, qrNonce string) error {
	// Get account from nonce and delete nonce
	accountID, found := qrCache.Get(qrNonce)
	if !found {
		return ErrorNotAuthenticated(c)
	}
	qrCache.Delete(qrNonce)
	qrCache.Delete(accountID.(string))

	BroadcastToRoom(accountID.(string), []byte("scanned"))

	conf := config.GetConfig()

	// Init OAuth2 flow with Google
	oauth2Config := oauth2.Config{
		ClientID:     conf.OauthConfig.GoogleClientID,
		ClientSecret: conf.OauthConfig.GoogleClientSecret,
		RedirectURL:  fmt.Sprintf("%s/auth/google/callback", conf.ApiConfig.BasePath),
		Scopes:       scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
	}

	// state is not nonce
	state := uuid.NewString()

	// Cache state
	stateCache.Set(state, accountID, cache.DefaultExpiration)
	redirectCache.Set(state, conf.ApiConfig.FrontendBasePath+"/borne/connected", cache.DefaultExpiration)

	hostDomainOption := oauth2.SetAuthURLParam("hd", "telecomnancy.net")
	// Redirect to Google
	url := oauth2Config.AuthCodeURL(state, oauth2.AccessTypeOffline, hostDomainOption)

	return c.Redirect(301, url)
}

type education struct {
	Promo  uint64 `json:"Promotion"`
	Spé    string `json:"Approfondissement"`
	Statut uint64 `json:"Statut"`
}

type googleUser struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	FirstName string `json:"given_name"`
	LastName  string `json:"family_name"`
	Link      string `json:"link"`
	Picture   string `json:"picture"`
}

// (GET /auth/google/callback)
func (s *Server) Callback(c echo.Context, params autogen.CallbackParams) error {
	// Get account from state and delete state
	accountID, found := stateCache.Get(params.State)
	if !found {
		return s.CallbackInpromptu(c, params)
	}
	stateCache.Delete(params.State)

	account, err := s.DBackend.GetAccount(c.Request().Context(), accountID.(string))
	if err != nil {
		if err != mongo.ErrNoDocuments {
			logrus.Error(err)
			return Error500(c)
		}
		// Check if account is onBoard
		acc, found := onBoardCache.Get(accountID.(string))
		if !found {
			return ErrorAccNotFound(c)
		}
		account = acc.(*models.Account)
	}

	conf := config.GetConfig()

	// Get token from Google
	oauth2Config := oauth2.Config{
		ClientID:     conf.OauthConfig.GoogleClientID,
		ClientSecret: conf.OauthConfig.GoogleClientSecret,
		RedirectURL:  fmt.Sprintf("%s/auth/google/callback", conf.ApiConfig.BasePath),
		Scopes:       scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
	}

	token, err := oauth2Config.Exchange(c.Request().Context(), params.Code)
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}

	// Get user from Google
	client := oauth2Config.Client(c.Request().Context(), token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}
	defer resp.Body.Close()

	usr := &googleUser{}
	err = json.NewDecoder(resp.Body).Decode(usr)
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}

	adminService, err := admin.NewService(c.Request().Context(), option.WithTokenSource(oauth2Config.TokenSource(c.Request().Context(), token)))
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}

	t, err := adminService.Users.Get(usr.ID).Projection("custom").CustomFieldMask("Education").ViewType("domain_public").Do()
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}
	edc := &education{}
	err = json.Unmarshal(t.CustomSchemas["Education"], edc)
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}

	account.FirstName = usr.FirstName
	account.LastName = usr.LastName
	account.EmailAddress = usr.Email
	account.GoogleId = &usr.ID
	account.GooglePicture = &usr.Picture

	if account.State == autogen.AccountNotOnBoarded {
		account.State = autogen.AccountOK

		err = s.DBackend.CreateAccount(c.Request().Context(), account)
		if err != nil {
			logrus.Error(err)
			return Error500(c)
		}

		// Delete ONBOARD cookie
		s.RemoveOnBoardCookie(c)
	} else {
		err = s.DBackend.UpdateAccount(c.Request().Context(), account)
		if err != nil {
			logrus.Error(err)
			return Error500(c)
		}
	}

	BroadcastToRoom(accountID.(string), []byte("connected"))

	r, found := redirectCache.Get(params.State)
	if !found {
		return Error500(c)
	}
	redirectCache.Delete(params.State)

	s.SetCookie(c, account)
	return c.Redirect(http.StatusPermanentRedirect, r.(string))
}

// (GET /auth/google/callback)
func (s *Server) CallbackInpromptu(c echo.Context, params autogen.CallbackParams) error {
	conf := config.GetConfig()

	// Get token from Google
	oauth2Config := oauth2.Config{
		ClientID:     conf.OauthConfig.GoogleClientID,
		ClientSecret: conf.OauthConfig.GoogleClientSecret,
		RedirectURL:  fmt.Sprintf("%s/auth/google/callback", conf.ApiConfig.BasePath),
		Scopes:       scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
	}

	token, err := oauth2Config.Exchange(c.Request().Context(), params.Code)
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}

	// Get user from Google
	client := oauth2Config.Client(c.Request().Context(), token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}
	defer resp.Body.Close()

	usr := &googleUser{}
	err = json.NewDecoder(resp.Body).Decode(usr)
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}

	account, err := s.DBackend.GetAccountByGoogle(c.Request().Context(), usr.ID)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return ErrorAccNotFound(c)
		}
		logrus.Error(err)
		return Error500(c)
	}

	adminService, err := admin.NewService(c.Request().Context(), option.WithTokenSource(oauth2Config.TokenSource(c.Request().Context(), token)))
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}

	t, err := adminService.Users.Get(usr.ID).Projection("custom").CustomFieldMask("Education").ViewType("domain_public").Do()
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}
	edc := &education{}
	err = json.Unmarshal(t.CustomSchemas["Education"], edc)
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}

	account.FirstName = usr.FirstName
	account.LastName = usr.LastName
	account.EmailAddress = usr.Email
	account.GoogleId = &usr.ID
	account.GooglePicture = &usr.Picture

	err = s.DBackend.UpdateAccount(c.Request().Context(), account)
	if err != nil {
		logrus.Error(err)
		return Error500(c)
	}

	r, found := redirectCache.Get(params.State)
	if !found {
		logrus.Error("no redirect found")
		return Error500(c)
	}
	redirectCache.Delete(params.State)

	s.SetCookie(c, account)
	return c.Redirect(http.StatusPermanentRedirect, r.(string))
}

// (POST /auth/card)
func (s *Server) ConnectCard(c echo.Context) error {
	// Check that header "X-Local-Token" is set to the local token
	if c.Request().Header.Get("X-Local-Token") != config.GetConfig().ApiConfig.LocalToken {
		return ErrorNotAuthenticated(c)
	}

	var param autogen.ConnectCardJSONBody
	err := c.Bind(&param)
	if err != nil {
		return Error400(c)
	}

	account, err := s.DBackend.GetAccountByCard(c.Request().Context(), param.CardId)
	if err != nil {
		if err != mongo.ErrNoDocuments {
			return Error500(c)
		}
		// Create default account with 1234 pin
		account = &models.Account{
			Account: autogen.Account{
				CardId:    param.CardId,
				Id:        uuid.New(),
				Role:      autogen.AccountStudent,
				PriceRole: autogen.AccountPriceNormal,
				State:     autogen.AccountNotOnBoarded,
			},
		}
		account.SetPin("1234")

		_, found := onBoardCache.Get(account.Account.Id.String())
		if found {
			return ErrorAccNotFound(c)
		}

		onBoardCache.Set(account.Account.Id.String(), account, cache.DefaultExpiration)
	}

	if !account.VerifyPin(param.CardPin) {
		return ErrorAccNotFound(c)
	}

	s.SetCookie(c, account)

	autogen.ConnectCard200JSONResponse{
		Account: &account.Account,
	}.VisitConnectCardResponse(c.Response())
	return nil
}

// (GET /auth/google)
func (s *Server) ConnectGoogle(c echo.Context, p autogen.ConnectGoogleParams) error {
	conf := config.GetConfig()

	// Get ?r=
	redirect := p.R

	// Check if it's a safe redirect (TODO: check if this is correct)
	if strings.HasPrefix(redirect, conf.ApiConfig.FrontendBasePath) {
		redirectCache.Set(redirect, true, cache.DefaultExpiration)
	}

	// Init OAuth2 flow with Google
	oauth2Config := oauth2.Config{
		ClientID:     conf.OauthConfig.GoogleClientID,
		ClientSecret: conf.OauthConfig.GoogleClientSecret,
		RedirectURL:  fmt.Sprintf("%s/auth/google/callback", conf.ApiConfig.BasePath),
		Scopes:       scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
	}

	// state is not nonce
	state := uuid.NewString()

	redirectCache.Set(state, redirect, cache.DefaultExpiration)

	hostDomainOption := oauth2.SetAuthURLParam("hd", "telecomnancy.net")
	// Redirect to Google
	url := oauth2Config.AuthCodeURL(state, oauth2.AccessTypeOffline, hostDomainOption)

	return c.Redirect(http.StatusTemporaryRedirect, url)
}

// (GET /logout)
func (s *Server) Logout(c echo.Context) error {
	s.RemoveCookies(c)

	autogen.Logout204Response{}.VisitLogoutResponse(c.Response())
	return nil
}
