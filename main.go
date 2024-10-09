package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var db *pgxpool.Pool

var ctx = context.Background()

func connectRedis() *redis.Client {
	// Connect to Redis at localhost:6379 (Docker)
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379", // Redis address
		Password: "",               // No password set
		DB:       0,                // Use default DB
	})

	// Test connection
	pong, err := rdb.Ping(ctx).Result()
	if err != nil {
		log.Fatalf("Could not connect to Redis: %v", err)
	}

	fmt.Printf("Redis connected: %s\n", pong)
	return rdb
}

func main() {
	var err error
	db, err = pgxpool.New(ctx, "postgres://root:secret@localhost:5432/root")
	if err != nil {
		fmt.Println("Unable to connect to database:", err)
	}
	fmt.Println("database connection successfull")
	defer db.Close()

	r := gin.Default()
	r.GET("/users", getUser)
	r.GET("/users/:id", getUserById)
	r.POST("/users", createUser)
	r.PATCH("/users/:id", updateUser)
	r.DELETE("/users/:id", deleteUser)

	if err := r.Run(":8080"); err != nil {
		log.Fatal(err)
	}

}

func getUser(c *gin.Context) {
	redis := connectRedis()
	users := []map[string]interface{}{}
	var retrievedUsers []map[string]interface{}

	value, err := redis.Get(ctx, "AllUsers").Result()
	if err != nil {
		fmt.Println("could not get value from the AllUsers key", err)
	} else {
		err = json.Unmarshal([]byte(value), &retrievedUsers)
		if err != nil {
			fmt.Println("Could not unmarshal data:", err)
		}
		fmt.Println("retrievedUsers is ", retrievedUsers)
	}
	if len(retrievedUsers) != 0 {
		users = retrievedUsers
	}

	if len(users) == 0 {
		rows, err := db.Query(ctx, "select * from users")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error ": err.Error()})
		}
		defer rows.Close()

		for rows.Next() {
			var id int
			var name string
			var age int
			if err := rows.Scan(&id, &name, &age); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			users = append(users, map[string]interface{}{"id": id, "name": name, "age": age})
		}

		allUsers, err := json.Marshal(users)
		fmt.Println("allUsers is ", string(allUsers))
		if err != nil {
			fmt.Println("Could not marshal data", err)
		}
		err = redis.Set(ctx, "AllUsers", string(allUsers), 10*time.Minute).Err()
		if err != nil {
			fmt.Println("could not set the AllUsers key", err)
		}
	}

	c.JSON(http.StatusOK, users)
}

func getUserById(c *gin.Context) {
	id := c.Param("id")
	row := db.QueryRow(ctx, "select * from users where id = $1", id)

	var user struct {
		UserId int    `json:"id"`
		Name   string `json:"name"`
		Age    int    `json:"age"`
	}

	err := row.Scan(&user.UserId, &user.Name, &user.Age)
	if err != nil {
		if err == pgx.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, user)
}

func createUser(c *gin.Context) {
	var user struct {
		Name string `json:"name" binding:"required"`
		Age  int    `json:"age" binding:"required min:4"`
	}

	if err := c.ShouldBindJSON(&user); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	fmt.Println(user.Name, user.Age)
	_, err := db.Exec(ctx, "INSERT INTO users (name, age) VALUES ($1, $2)", user.Name, user.Age)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "User created"})
}

func updateUser(c *gin.Context) {
	id := c.Param("id")

	var updateData struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	// Bind JSON request body to updateData struct
	if err := c.ShouldBindJSON(&updateData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON format"})
		return
	}

	if updateData.Name == "" && updateData.Age == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provide values to required fields to update"})
		return
	}

	url := fmt.Sprintf("http://localhost:8080/users/%v", id)
	fmt.Println("url is ", url)
	// Make the GET request
	resp, err := http.Get(url)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	// Print the response body
	fmt.Println(string(body))

	if updateData.Name == "" {
		nameFromMidQuery := strings.Split(strings.Split(string(body), ",")[1], ":")[1]
		updateData.Name = nameFromMidQuery[1 : len(nameFromMidQuery)-1]
	}
	if updateData.Age == 0 {
		ageFromMidQuery := strings.Split(strings.Split(string(body), ",")[2], ":")[1]
		updateData.Age, _ = strconv.Atoi(ageFromMidQuery[:len(ageFromMidQuery)-1])
	}

	// Execute the update query
	query := "UPDATE users SET name = $1, age = $2 WHERE id = $3"
	result, err := db.Exec(ctx, query, updateData.Name, updateData.Age, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Check if any row was updated
	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	// Return success response
	c.JSON(http.StatusOK, gin.H{"message": "User updated successfully"})
}

func deleteUser(c *gin.Context) {
	id := c.Param("id")

	// Execute the delete query
	query := "DELETE FROM users WHERE id = $1"
	result, err := db.Exec(ctx, query, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Check if any row was deleted
	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	// Return success response
	c.JSON(http.StatusOK, gin.H{"message": "User deleted successfully"})
}
