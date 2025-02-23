package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	obj "orchestrator/internal/entities"
	"orchestrator/internal/parser"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func startGarbageCollector() {
	ticker := time.NewTicker(3 * time.Minute)
	go func() {
		for range ticker.C {
			now := time.Now()
			expressionsMap := obj.Expressions.GetAll()
			for key, expr := range expressionsMap {
				task, ok := expr.(obj.ClientResponse)
				if ok && (task.Status == "Done" || task.Status == "Fail") && now.Sub(task.GetTimestamp()) > 3*time.Minute {
					obj.Expressions.Delete(key)
				}
			}
		}
	}()
}

// isValidExpression checks if the expression is valid
func isValidExpression(expression string) bool {
	re := regexp.MustCompile("^[\\d+\\-*/\\s]+$")
	return re.MatchString(expression)
}

func agentMiddleware(getHandler, postHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getHandler.ServeHTTP(w, r)
		case http.MethodPost:
			postHandler.ServeHTTP(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func getHandler(w http.ResponseWriter, r *http.Request) {
	if obj.Tasks.IsEmpty() {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	task := obj.Tasks.Dequeue()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(task); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func postHandler(w http.ResponseWriter, r *http.Request) {
	var taskRes obj.ClientResponse
	if err := json.NewDecoder(r.Body).Decode(&taskRes); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	node := obj.ParsersTree.Search(taskRes.Id)
	// TODO: invalid data, code 422?
	if node == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	ch := node.Value.(*chan float64)
	*ch <- taskRes.Result
	w.WriteHeader(http.StatusOK)
}

// calculateHandler handles the /api/v1/calculate endpoint
func calculateHandler(w http.ResponseWriter, r *http.Request) {
	var clientRequest obj.ClientRequest
	var clientResponse obj.ClientResponse
	err := json.NewDecoder(r.Body).Decode(&clientRequest)
	if err != nil {
		clientResponse.Error = "Internal server error"
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !isValidExpression(clientRequest.Expression) {
		clientResponse.Error = "Expression is not valid"
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	clientResponse.Id = obj.TasksLastID.GetValue()
	w.WriteHeader(http.StatusCreated)
	w.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(w).Encode(clientResponse); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	obj.TasksLastID.Increment()
	obj.Wg.Add(1)
	go parser.Parse(clientRequest.Expression, clientResponse.Id)
}

func expressionHandler(w http.ResponseWriter, r *http.Request) {
	expressionsMap := obj.Expressions.GetAll()
	expressions := make([]obj.ClientResponse, 0, len(expressionsMap))

	for _, expr := range expressionsMap {
		task, ok := expr.(obj.ClientResponse)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		expressions = append(expressions, task)
	}
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"expressions": expressions}); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func expressionIDHandler(w http.ResponseWriter, r *http.Request) {
	urlParts := strings.Split(r.URL.Path, "/")
	idStr := urlParts[len(urlParts)-1]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusInternalServerError)
		return
	}

	expression := obj.Expressions.Get(strconv.Itoa(id))
	if expression == nil {
		http.Error(w, "Expression not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(w).Encode(expression); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

// StartServer starts the server on port 8080 and listens for incoming requests
func StartServer() error {
	mux := http.NewServeMux()
	// Start the garbage collector to remove old tasks
	startGarbageCollector()
	// Handle functions for client requests
	mux.HandleFunc("/api/v1/calculate", calculateHandler)
	mux.HandleFunc("/api/v1/expressions", expressionHandler)
	mux.HandleFunc("/api/v1/expressions/", expressionIDHandler)

	// Handle functions for agent requests
	mux.Handle("/internal/task", agentMiddleware(http.HandlerFunc(getHandler), http.HandlerFunc(postHandler)))

	// Start the server
	err := http.ListenAndServe(":8080", mux)
	if err != nil {
		return fmt.Errorf("could not start server: %v", err)
	}
	obj.Wg.Wait()
	return nil
}
