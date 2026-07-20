package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/epistola-app/portablevps/internal/config"
	"github.com/epistola-app/portablevps/internal/keystore"
)

const (
	legacyAdminPrivateRel = ".local/ssh/cloud-admin_ed25519"
	legacyAdminPublicRel  = "keys/cloud-admin.pub"
)

type adminIdentity struct {
	privateRel string
	publicRel  string
}

func resolveAdminIdentity(ctx *config.Context, server, privateOverride, publicOverride string) adminIdentity {
	privateOverride = firstNonEmptyCli(privateOverride, os.Getenv("CLOUD_ADMIN_KEY"))
	publicOverride = firstNonEmptyCli(publicOverride, os.Getenv("CLOUD_ADMIN_PUBKEY"))

	if privateOverride != "" {
		return adminIdentity{
			privateRel: privateOverride,
			publicRel:  publicOverride,
		}
	}

	publicRel := publicOverride
	if publicRel == "" {
		publicRel = firstExistingRel(ctx.RepoRoot,
			filepath.Join("keys", "servers", server+"-admin.pub"),
			filepath.Join("keys", server+"-admin.pub"),
			legacyAdminPublicRel,
		)
	}

	privateRel := privateForPublic(server, publicRel)
	if privateRel == "" || !fileExistsCli(repoRelOrAbs(ctx.RepoRoot, privateRel)) {
		privateRel = firstExistingRel(ctx.RepoRoot,
			filepath.Join(".local", "ssh", "servers", server+"_ed25519"),
			legacyAdminPrivateRel,
		)
	}

	return adminIdentity{
		privateRel: privateRel,
		publicRel:  publicRel,
	}
}

func (i adminIdentity) ref(ctx *config.Context, server string, usePasswordManager bool) keystore.Ref {
	ref := keystore.Ref{
		FilePath: repoRelOrAbs(ctx.RepoRoot, i.privateRel),
		PubPath:  repoRelOrAbs(ctx.RepoRoot, i.publicRel),
	}
	if usePasswordManager {
		ref.OpItem = keystore.DefaultOpItem(ctx.Vault, server)
	}
	return ref
}

func firstExistingRel(root string, candidates ...string) string {
	for _, candidate := range candidates {
		if candidate != "" && fileExistsCli(repoRelOrAbs(root, candidate)) {
			return candidate
		}
	}
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func privateForPublic(server, publicRel string) string {
	clean := filepath.ToSlash(publicRel)
	switch clean {
	case "":
		return ""
	case legacyAdminPublicRel:
		return legacyAdminPrivateRel
	case filepath.ToSlash(filepath.Join("keys", "servers", server+"-admin.pub")),
		filepath.ToSlash(filepath.Join("keys", server+"-admin.pub")):
		return filepath.Join(".local", "ssh", "servers", server+"_ed25519")
	default:
		if strings.HasSuffix(publicRel, ".pub") {
			return strings.TrimSuffix(publicRel, ".pub")
		}
		return ""
	}
}

func firstNonEmptyCli(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
