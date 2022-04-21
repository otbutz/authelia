package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/ory/fosite"

	"github.com/authelia/authelia/v4/internal/middlewares"
	"github.com/authelia/authelia/v4/internal/model"
	"github.com/authelia/authelia/v4/internal/oidc"
	"github.com/authelia/authelia/v4/internal/session"
	"github.com/authelia/authelia/v4/internal/storage"
	"github.com/authelia/authelia/v4/internal/utils"
)

func handleOIDCAuthorizationConsent(ctx *middlewares.AutheliaCtx, rootURI string, client *oidc.Client,
	userSession session.UserSession, subject uuid.UUID,
	rw http.ResponseWriter, r *http.Request, requester fosite.AuthorizeRequester) (consent *model.OAuth2ConsentSession, handled bool) {
	if userSession.ConsentChallengeID != nil {
		return handleOIDCAuthorizationConsentWithChallengeID(ctx, rootURI, client, userSession, rw, r, requester)
	}

	return handleOIDCAuthorizationConsentOrGenerate(ctx, rootURI, client, userSession, subject, rw, r, requester)
}

func handleOIDCAuthorizationConsentWithChallengeID(ctx *middlewares.AutheliaCtx, rootURI string, client *oidc.Client,
	userSession session.UserSession,
	rw http.ResponseWriter, r *http.Request, requester fosite.AuthorizeRequester) (consent *model.OAuth2ConsentSession, handled bool) {
	var (
		err error
	)

	if consent, err = ctx.Providers.StorageProvider.LoadOAuth2ConsentSessionByChallengeID(ctx, *userSession.ConsentChallengeID); err != nil {
		ctx.Logger.Errorf("Authorization Request with id '%s' on client with id '%s' could not be processed: error occurred during consent session lookup: %+v", requester.GetID(), requester.GetClient().GetID(), err)

		ctx.Providers.OpenIDConnect.Fosite.WriteAuthorizeError(rw, requester, fosite.ErrServerError.WithHint("Failed to lookup consent session."))

		userSession.ConsentChallengeID = nil

		if err = ctx.SaveSession(userSession); err != nil {
			ctx.Logger.Errorf("Authorization Request with id '%s' on client with id '%s' could not be processed: error occurred unlinking consent session challenge id: %+v", requester.GetID(), requester.GetClient().GetID(), err)
		}

		return nil, true
	}

	if consent.Responded() {
		userSession.ConsentChallengeID = nil

		if err = ctx.SaveSession(userSession); err != nil {
			ctx.Logger.Errorf("Authorization Request with id '%s' on client with id '%s' could not be processed: error occurred saving session: %+v", requester.GetID(), client.GetID(), err)

			ctx.Providers.OpenIDConnect.Fosite.WriteAuthorizeError(rw, requester, fosite.ErrServerError.WithHint("Could not save the session."))

			return nil, true
		}

		if consent.Granted {
			ctx.Logger.Errorf("Authorization Request with id '%s' on client with id '%s' could not be processed: this consent session with challenge id '%s' was already granted", requester.GetID(), client.GetID(), consent.ChallengeID.String())

			ctx.Providers.OpenIDConnect.Fosite.WriteAuthorizeError(rw, requester, fosite.ErrServerError.WithHint("Authorization already granted."))

			return nil, true
		}

		ctx.Logger.Debugf("Authorization Request with id '%s' loaded consent session with id '%d' and challenge id '%s' for client id '%s' and subject '%s' and scopes '%s'", requester.GetID(), consent.ID, consent.ChallengeID.String(), client.GetID(), consent.Subject.String(), strings.Join(requester.GetRequestedScopes(), " "))

		if consent.IsDenied() {
			ctx.Logger.Warnf("Authorization Request with id '%s' and challenge id '%s' for client id '%s' and subject '%s' and scopes '%s' was not denied by the user durng the consent session", requester.GetID(), consent.ChallengeID.String(), client.GetID(), consent.Subject.String(), strings.Join(requester.GetRequestedScopes(), " "))

			ctx.Providers.OpenIDConnect.Fosite.WriteAuthorizeError(rw, requester, fosite.ErrAccessDenied)

			return nil, true
		}

		return consent, false
	}

	handleOIDCAuthorizationConsentRedirect(rootURI, client, userSession, rw, r)

	return consent, true
}

func handleOIDCAuthorizationConsentOrGenerate(ctx *middlewares.AutheliaCtx, rootURI string, client *oidc.Client,
	userSession session.UserSession, subject uuid.UUID,
	rw http.ResponseWriter, r *http.Request, requester fosite.AuthorizeRequester) (consent *model.OAuth2ConsentSession, handled bool) {
	var (
		rows             *storage.ConsentSessionRows
		scopes, audience []string
		err              error
	)

	scopes, audience = getExpectedScopesAndAudience(requester)

	ctx.Logger.Tracef("Authorization Request with id '%s' on client with id '%s' is checking for pre-configured consent settions for subject %s with scopes %+v and audience %+v", requester.GetID(), requester.GetClient().GetID(), subject.String(), scopes, audience)

	if rows, err = ctx.Providers.StorageProvider.LoadOAuth2ConsentSessionsPreConfigured(ctx, client.GetID(), subject); err != nil {
		ctx.Logger.Errorf("Authorization Request with id '%s' on client with id '%s' had error looking up pre-configured consent sessions: %+v", requester.GetID(), requester.GetClient().GetID(), err)
	}

	i := 0

	defer rows.Close()

	for rows.Next() {
		i++

		if consent, err = rows.Get(); err != nil {
			ctx.Logger.Errorf("Authorization Request with id '%s' on client with id '%s' had error looking up pre-configured consent sessions: %+v", requester.GetID(), requester.GetClient().GetID(), err)

			ctx.Providers.OpenIDConnect.Fosite.WriteAuthorizeError(rw, requester, fosite.ErrServerError.WithHint("Could not lookup pre-configured consent sessions."))

			return nil, true
		}

		expires := int64(0)
		if consent.ExpiresAt != nil {
			expires = consent.ExpiresAt.Unix()
		}

		ctx.Logger.Tracef("Authorization Request with id '%s' on client with id '%s' is checking potential pre-configured consent settion for subject %s with session id %s which expires at %d with scopes %+v and audience %+v", requester.GetID(), requester.GetClient().GetID(), subject.String(), consent.ChallengeID.String(), expires, consent.GrantedScopes, consent.GrantedAudience)

		if consent.HasExactGrants(scopes, audience) && consent.CanGrant() {
			ctx.Logger.Tracef("Authorization Request with id '%s' on client with id '%s' matched pre-configured consent settion for subject %s with session id %s", requester.GetID(), requester.GetClient().GetID(), subject.String(), consent.ChallengeID.String())

			break
		}

		ctx.Logger.Tracef("Authorization Request with id '%s' on client with id '%s' did not match pre-configured consent settion for subject %s with session id %s", requester.GetID(), requester.GetClient().GetID(), subject.String(), consent.ChallengeID.String())
	}

	if consent != nil && consent.HasExactGrants(scopes, audience) && consent.CanGrant() {
		ctx.Logger.Tracef("Authorization Request with id '%s' on client with id '%s' is finished checking (sucessfully) for pre-configured consent settions for subject %s with scopes %+v and audience %+v with %d rows checked", requester.GetID(), requester.GetClient().GetID(), subject.String(), scopes, audience, i)

		return consent, false
	}

	ctx.Logger.Tracef("Authorization Request with id '%s' on client with id '%s' is finished checking (unsucessfully) for pre-configured consent settions for subject %s with scopes %+v and audience %+v with %d rows checked", requester.GetID(), requester.GetClient().GetID(), subject.String(), scopes, audience, i)

	if consent, err = model.NewOAuth2ConsentSession(subject, requester); err != nil {
		ctx.Logger.Errorf("Authorization Request with id '%s' on client with id '%s' could not be processed: error occurred generating consent: %+v", requester.GetID(), requester.GetClient().GetID(), err)

		ctx.Providers.OpenIDConnect.Fosite.WriteAuthorizeError(rw, requester, fosite.ErrServerError.WithHint("Could not generate the consent session."))

		return nil, true
	}

	if err = ctx.Providers.StorageProvider.SaveOAuth2ConsentSession(ctx, *consent); err != nil {
		ctx.Logger.Errorf("Authorization Request with id '%s' on client with id '%s' could not be processed: error occurred saving consent session: %+v", requester.GetID(), client.GetID(), err)

		ctx.Providers.OpenIDConnect.Fosite.WriteAuthorizeError(rw, requester, fosite.ErrServerError.WithHint("Could not save the consent session."))

		return nil, true
	}

	userSession.ConsentChallengeID = &consent.ChallengeID

	if err = ctx.SaveSession(userSession); err != nil {
		ctx.Logger.Errorf("Authorization Request with id '%s' on client with id '%s' could not be processed: error occurred saving user session for consent: %+v", requester.GetID(), client.GetID(), err)

		ctx.Providers.OpenIDConnect.Fosite.WriteAuthorizeError(rw, requester, fosite.ErrServerError.WithHint("Could not save the user session."))

		return nil, true
	}

	handleOIDCAuthorizationConsentRedirect(rootURI, client, userSession, rw, r)

	return consent, true
}

func handleOIDCAuthorizationConsentRedirect(destination string, client *oidc.Client, userSession session.UserSession, rw http.ResponseWriter, r *http.Request) {
	if client.IsAuthenticationLevelSufficient(userSession.AuthenticationLevel) {
		destination = fmt.Sprintf("%s/consent", destination)
	}

	http.Redirect(rw, r, destination, http.StatusFound)
}

func getExpectedScopesAndAudience(requester fosite.Requester) (scopes, audience []string) {
	audience = requester.GetRequestedAudience()
	if !utils.IsStringInSlice(requester.GetClient().GetID(), audience) {
		audience = append(audience, requester.GetClient().GetID())
	}

	return requester.GetRequestedScopes(), audience
}
