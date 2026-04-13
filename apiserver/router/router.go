package router

import (
	"net/http"

	"github.com/gabriel-samfira/localshow/apiserver/controllers"
)

func NewAPIRouter(han *controllers.APIController) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", han.LandingPage)
	return mux
}
