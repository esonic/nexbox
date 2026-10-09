package api

import (
	"net/http"

	"github.com/esonic/nexbox/gitrepo"
)

func writeRepositoryOperationUnsupported(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
		"error": err.Error(), "code": gitrepo.CodeUnsupported,
	})
}
