package vault

import (
	"context"
	"fmt"

	"github.com/intelops/go-common/credentials"
	log "github.com/sirupsen/logrus"
)

const (
	credentialType = "generic"
)

// GetGenericCredential entity - azsecret/azsecret
//
func GetGenericCredential(ctx context.Context, entity, credIdentifier string) (map[string]string, error) {
	credReader, err := credentials.NewCredentialReader(ctx)
	if err != nil {
		log.Errorf("Failed while creating Credential Reader, error : %v", err)
		return nil, fmt.Errorf("failed while creating Credential Reader, error : %v", err)
	}

	cred, err := credReader.GetCredential(context.Background(), credentialType, entity, credIdentifier)
	if err != nil {
		log.Errorf("Failed while get credential, error : %v", err)
		return nil, fmt.Errorf("failed while get credential, error : %v", err)
	}

	return cred, nil
}
