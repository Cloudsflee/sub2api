package service

import (
	"context"
	"net/http"
	"strings"
)

// openAIQuotaAccountID returns the workspace identifier accepted by ChatGPT's
// quota and Codex endpoints. Older imported OAuth rows persisted the same poid
// only as organization_id, so request paths that intentionally support those
// rows must use this resolver consistently.
func openAIQuotaAccountID(account *Account) string {
	if account == nil || !account.IsOpenAIOAuth() {
		return ""
	}
	if accountID := strings.TrimSpace(account.GetCredential("chatgpt_account_id")); accountID != "" {
		return accountID
	}
	return strings.TrimSpace(account.GetCredential("organization_id"))
}

func setOpenAIQuotaAccountHeaders(headers http.Header, account *Account) {
	setOpenAIChatGPTAccountHeaders(headers, account)
	if headers == nil || headers.Get("chatgpt-account-id") != "" {
		return
	}
	if accountID := openAIQuotaAccountID(account); accountID != "" {
		headers.Set("chatgpt-account-id", accountID)
	}
}

func setOpenAIChatGPTAccountHeaders(headers http.Header, account *Account) {
	if headers == nil || account == nil || !account.IsOpenAIOAuthLike() {
		return
	}
	if chatgptAccountID := account.GetChatGPTAccountID(); chatgptAccountID != "" {
		headers.Set("chatgpt-account-id", chatgptAccountID)
	}
	if account.IsChatGPTAccountFedRAMP() {
		headers.Set("x-openai-fedramp", "true")
	} else {
		headers.Del("x-openai-fedramp")
	}
}

// resolveAndSetOpenAIChatGPTAccountHeaders 解析 spark 影子账号至其母账号（凭据透传），
// 再调用 setOpenAIChatGPTAccountHeaders 写入 chatgpt-account-id / x-openai-fedramp 头。
// 普通账号（非影子）为直通，行为与直接调用 setOpenAIChatGPTAccountHeaders 一致。
func resolveAndSetOpenAIChatGPTAccountHeaders(ctx context.Context, repo AccountRepository, headers http.Header, account *Account) error {
	credAccount, err := resolveCredentialAccount(ctx, repo, account)
	if err != nil {
		return err
	}
	setOpenAIChatGPTAccountHeaders(headers, credAccount)
	return nil
}
