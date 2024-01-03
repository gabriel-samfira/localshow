package router

import (
	"github.com/gabriel-samfira/localshow/apiserver/controllers"
	"github.com/gorilla/mux"
)

func NewAPIRouter(han *controllers.APIController) *mux.Router {
	router := mux.NewRouter()
	router.PathPrefix("/").HandlerFunc(han.LandingPage).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")
	return router
}
