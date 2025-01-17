package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
	"bytes"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

)

var client *mongo.Client

func main() {
	// Create a new MongoDB client
	var err error
	client, err = mongo.NewClient(options.Client().ApplyURI("mongodb://task-mongodb:27017"))
	if err != nil {
		log.Fatal(err)
	}

	// Connect to MongoDB
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = client.Connect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Disconnect(ctx)

	// Check if the database and collection exist, create them if they don't
	err = ensureDatabaseAndCollection(client)
	if err != nil {
		log.Fatal(err)
	}

	// Create a new HTTP server
	mux := http.NewServeMux()

mux.Handle("/tasks/list", http.HandlerFunc(listTasks))
mux.Handle("/tasks/create", http.HandlerFunc(createTask))
mux.Handle("/tasks/get/", http.HandlerFunc(getTask))
mux.Handle("/tasks/update/", http.HandlerFunc(updateTask))
mux.Handle("/tasks/remove/", authMiddleware(adminMiddleware(http.HandlerFunc(removeTask))))
mux.Handle("/tasks/removeAllTasks", http.HandlerFunc(removeAllTasks))
mux.Handle("/tasks/listByUser/", http.HandlerFunc(listTasksByUser))

	// Start the server
	log.Println("Task Service listening on port 8002...")
	log.Fatal(http.ListenAndServe(":8002", mux))
}

func ensureDatabaseAndCollection(client *mongo.Client) error {
	dbName := "taskmanagement"
	collectionName := "tasks"

	// Check if the database exists
	databases, err := client.ListDatabaseNames(context.Background(), bson.M{})
	if err != nil {
		return err
	}

	dbExists := false
	for _, db := range databases {
		if db == dbName {
			dbExists = true
			break
		}
	}

	if !dbExists {
		// Create the database if it doesn't exist
		err = client.Database(dbName).CreateCollection(context.Background(), collectionName)
		if err != nil {
			return err
		}
		log.Printf("Created database '%s' and collection '%s'", dbName, collectionName)
	} else {
		// Check if the collection exists
		collections, err := client.Database(dbName).ListCollectionNames(context.Background(), bson.M{})
		if err != nil {
			return err
		}

		collectionExists := false
		for _, coll := range collections {
			if coll == collectionName {
				collectionExists = true
				break
			}
		}

		if !collectionExists {
			// Create the collection if it doesn't exist
			err = client.Database(dbName).CreateCollection(context.Background(), collectionName)
			if err != nil {
				return err
			}
			log.Printf("Created collection '%s' in database '%s'", collectionName, dbName)
		}
	}

	return nil
}

type Task struct {
    ID          primitive.ObjectID `bson:"_id" json:"id"`
    Title       string             `bson:"title" json:"title"`
    Description string             `bson:"description" json:"description"`
    AssignedTo  primitive.ObjectID `bson:"assigned_to" json:"assigned_to"`
    Status      string             `bson:"status" json:"status"`
    Hours       float64             `bson:"hours" json:"hours"`
    StartDate   time.Time          `bson:"start_date" json:"start_date"`
    EndDate     time.Time          `bson:"end_date" json:"end_date"`
    InvoiceID   primitive.ObjectID `bson:"invoice_id,omitempty" json:"invoice_id,omitempty"`
    ParentTask  *primitive.ObjectID `bson:"parent_task,omitempty" json:"parent_task,omitempty"`
}

type Billing struct {
    ID     primitive.ObjectID `bson:"_id" json:"id"`
    UserID primitive.ObjectID `bson:"user_id" json:"user_id"`
    TaskID primitive.ObjectID `bson:"task_id" json:"task_id"`
    Hours  float64             `bson:"hours" json:"hours"`
    Amount float64             `bson:"amount" json:"amount"`
}

func createTask(w http.ResponseWriter, req *http.Request) {
    var task Task
    err := json.NewDecoder(req.Body).Decode(&task)
    if err != nil {
        http.Error(w, "Invalid request body", http.StatusBadRequest)
        return
    }

    // Check for overlapping tasks
    var overlappingTasks []Task
    filter := bson.M{
        "assigned_to": task.AssignedTo,
        "end_date": bson.M{"$gt": task.StartDate},
        "start_date": bson.M{"$lt": task.EndDate},
    }
    cursor, err := client.Database("taskmanagement").Collection("tasks").Find(context.TODO(), filter)
    if err != nil {
        http.Error(w, "Database query error", http.StatusInternalServerError)
        return
    }
    if cursor.All(context.Background(), &overlappingTasks); len(overlappingTasks) > 0 {
        // Append a warning to the task description indicating overlapping dates
        task.Description += " Warning: This task overlaps with existing task(s)."
    }

    task.ID = primitive.NewObjectID()
    _, err = client.Database("taskmanagement").Collection("tasks").InsertOne(context.TODO(), task)
    if err != nil {
        http.Error(w, "Failed to create task", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(task)
	log.Printf("Task created successfully: %+v", task)
}




func getTask(w http.ResponseWriter, req *http.Request) {
	taskID := req.URL.Path[len("/tasks/get/"):]
	objectID, err := primitive.ObjectIDFromHex(taskID)
	if err != nil {
		http.Error(w, "Invalid task ID", http.StatusBadRequest)
		return
	}

	var task Task
	err = client.Database("taskmanagement").Collection("tasks").FindOne(context.TODO(), bson.M{"_id": objectID}).Decode(&task)
	if err != nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	var subtasks []Task
	cursor, err := client.Database("taskmanagement").Collection("tasks").Find(context.TODO(), bson.M{"parent_task": objectID})
	if err == nil {
		defer cursor.Close(context.Background())
		cursor.All(context.Background(), &subtasks)
	}

	response := struct {
		Task     Task   `json:"task"`
		Subtasks []Task `json:"subtasks"`
	}{
		Task:     task,
		Subtasks: subtasks,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func updateTask(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	taskID := req.URL.Path[len("/tasks/update/"):]
	objectID, err := primitive.ObjectIDFromHex(taskID)
	if err != nil {
		http.Error(w, "Invalid task ID", http.StatusBadRequest)
		return
	}

	var updates map[string]interface{}
	err = json.NewDecoder(req.Body).Decode(&updates)
	if err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

// Prepare update document
    updateDoc := bson.M{"$set": bson.M{}}
    for key, value := range updates {
        // Ensure only allowed fields are updated and handle date parsing
        switch key {
        case "title", "description", "assigned_to", "status", "hours":
            updateDoc["$set"].(bson.M)[key] = value
        case "start_date", "end_date":
            if dateString, ok := value.(string); ok {
                parsedDate, err := time.Parse(time.RFC3339, dateString)
                if err != nil {
                    http.Error(w, "Invalid date format", http.StatusBadRequest)
                    return
                }
                updateDoc["$set"].(bson.M)[key] = parsedDate
            }
        case "parent_task":
            if parentTaskIDString, ok := value.(string); ok {
                parentTaskID, err := primitive.ObjectIDFromHex(parentTaskIDString)
                if err != nil {
                    http.Error(w, "Invalid parent task ID", http.StatusBadRequest)
                    return
                }
                updateDoc["$set"].(bson.M)["parent_task"] = parentTaskID
            }
        }
    }

	collection := client.Database("taskmanagement").Collection("tasks")
	// Fetch the current task to compare changes
	var currentTask Task
	err = collection.FindOne(context.TODO(), bson.M{"_id": objectID}).Decode(&currentTask)
	if err != nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	// Handle InvoiceID creation if task status changes to 'done'
if currentTask.Status != "done" && updates["status"] == "done" {
    invoiceID, err := createInvoiceInBillingService(currentTask)
    if err != nil {
        log.Printf("Failed to create invoice: %v", err)
        http.Error(w, "Failed to create invoice", http.StatusInternalServerError)
        return
    }

    updateDoc["$set"].(bson.M)["invoice_id"] = invoiceID
    log.Printf("Task updated to 'done'. New InvoiceID: %v generated", invoiceID)
}

	_, err = collection.UpdateOne(context.TODO(), bson.M{"_id": objectID}, updateDoc)
	if err != nil {
		http.Error(w, "Failed to update task", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func removeTask(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	taskID := req.URL.Path[len("/tasks/remove/"):]
	objectID, err := primitive.ObjectIDFromHex(taskID)
	if err != nil {
		http.Error(w, "Invalid task ID", http.StatusBadRequest)
		return
	}

	collection := client.Database("taskmanagement").Collection("tasks")
	filter := bson.M{"_id": objectID}

	_, err = collection.DeleteOne(context.TODO(), filter)
	if err != nil {
		http.Error(w, "Failed to remove task", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func listTasks(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	collection := client.Database("taskmanagement").Collection("tasks")
	cursor, err := collection.Find(context.TODO(), bson.M{})
	if err != nil {
		http.Error(w, "Failed to list tasks", http.StatusInternalServerError)
		return
	}
	defer cursor.Close(context.Background())

	var tasks []Task
	err = cursor.All(context.Background(), &tasks)
	if err != nil {
		http.Error(w, "Failed to decode tasks", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

func listTasksByUser(w http.ResponseWriter, req *http.Request) {
	userID := req.URL.Path[len("/tasks/listByUser/"):] // Assuming the endpoint is like /tasks/listByUser/<UserID>
	objectID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	filter := bson.M{"assigned_to": objectID}

	collection := client.Database("taskmanagement").Collection("tasks")
	cursor, err := collection.Find(context.TODO(), filter)
	if err != nil {
		http.Error(w, "Failed to list tasks", http.StatusInternalServerError)
		return
	}
	defer cursor.Close(context.Background())

	var tasks []Task
	if err = cursor.All(context.Background(), &tasks); err != nil {
		http.Error(w, "Failed to decode tasks", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

func removeAllTasks(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	collection := client.Database("taskmanagement").Collection("tasks")

	_, err := collection.DeleteMany(context.TODO(), bson.M{})
	if err != nil {
		http.Error(w, "Failed to remove all tasks", http.StatusInternalServerError)
		return
	}
}

func createInvoiceInBillingService(task Task) (primitive.ObjectID, error) {
    hourlyRate := 100.0  // Ensure this is defined or passed correctly
    amount := task.Hours * hourlyRate

    billing := Billing{
        UserID: task.AssignedTo,
        TaskID: task.ID,
        Hours:  task.Hours,
        Amount: amount,
    }

    jsonData, err := json.Marshal(billing)
    if err != nil {
        log.Printf("Error marshalling invoice data: %v", err)
        return primitive.NilObjectID, err
    }

    req, err := http.NewRequest("POST", "http://api-gateway:8000/billings/createForTaskService", bytes.NewBuffer(jsonData))
    if err != nil {
        log.Printf("Error creating request: %v", err)
        return primitive.NilObjectID, err
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-Task-Service", "your-task-service-secret")

    log.Printf("Sending request to billing service with headers: %+v and body: %s", req.Header, jsonData)

    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        log.Printf("Error sending request to billing service: %v", err)
        return primitive.NilObjectID, err
    }
    defer resp.Body.Close()

    log.Printf("Billing service responded with status: %d", resp.StatusCode)

    if resp.StatusCode != http.StatusOK {
        log.Printf("Failed to create invoice, billing service responded with status: %d", resp.StatusCode)
        return primitive.NilObjectID, fmt.Errorf("billing service error: %d", resp.StatusCode)
    }

    var createdBilling Billing
    if err := json.NewDecoder(resp.Body).Decode(&createdBilling); err != nil {
        log.Printf("Error decoding response from billing service: %v", err)
        return primitive.NilObjectID, err
    }

    return createdBilling.ID, nil
}
