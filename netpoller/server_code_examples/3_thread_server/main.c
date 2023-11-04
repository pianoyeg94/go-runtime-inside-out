#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <signal.h>
#include <sys/socket.h>
#include <netdb.h>
#include <unistd.h>
#include <pthread.h> 

static const uint16_t INTERFACE = INADDR_ANY;
static const int PORT = 8082;
static const int BACKLOG = SOMAXCONN;

static const char RESPONSE[] = 
"HTTP/1.1 200 OK\r\n"
"Date: Mon, 27 Jul 2009 12:28:53 GMT\r\n"
"Server: Apache/2.2.14 (Win32)\r\n"
"Last-Modified: Wed, 22 Jul 2009 19:15:56 GMT\r\n"
"Content-Length: 88\r\n"
"Content-Type: text/html\r\n"
"\r\n"
"<html><body><h1>Hello, World!</h1></body></html>\r\n";

static int start_server();
static void *serve_client(void*);
static void read_request(int);
static void send_response(int);

static int listen_fd;

void sig_handler(int sig_num) {
    close(listen_fd);
    exit(sig_num);
}

int main() {
    signal(SIGTERM, sig_handler);
    signal(SIGINT, sig_handler);

    listen_fd = start_server();
    if (listen_fd < 0) 
        return 1;

    printf("Server listening on 0.0.0.0:%d\n", PORT);

    // pthread_vector_init(&thread_ids);
    while(1) {
        int *client_fd = malloc(sizeof(int));
        *client_fd = accept(listen_fd, (struct sockaddr*) NULL, (socklen_t*) NULL);
        if (*client_fd < 0) {
            perror("error accepting client connection");
            free(client_fd);
            continue;
        }

        pthread_t thread_id;
        if(pthread_create(&thread_id, NULL, &serve_client, (void*) client_fd) < 0) {
            perror("error handling client connection");
            continue;
        }
    }
}

static int start_server() {
    struct sockaddr_in bind_addr;
    bzero(&bind_addr, sizeof(bind_addr));

    bind_addr.sin_family = AF_INET;
    bind_addr.sin_addr.s_addr = htons(INTERFACE);
    bind_addr.sin_port = htons(PORT);

    int listen_fd = socket(AF_INET, SOCK_STREAM, 0);
    if (setsockopt(listen_fd, SOL_SOCKET, SO_REUSEADDR, &(int){1}, sizeof(int)) < 0)
        perror("error setting socket option");

    if (bind(listen_fd, (struct sockaddr*) &bind_addr, sizeof(bind_addr)) != 0) {
        perror("error binding socket to address");
        return -1;
    }

    if (listen(listen_fd, BACKLOG) != 0) {
        perror("error listening on address");
        return -1;
    }

    return listen_fd;
}

static void *serve_client(void *fd) {
    pthread_detach(pthread_self());

    int client_fd = *(int*) fd;
    free(fd);

    read_request(client_fd);
    send_response(client_fd);

    shutdown(client_fd, SHUT_RDWR);
    close(client_fd);
   
    return 0;
}

static void read_request(int client_fd) {
    char throw_away[4096];
    memset(throw_away, 0, sizeof(throw_away));
    int n = read(client_fd, throw_away, sizeof(throw_away)-1);
    if (n <= 0)
        perror("error reading request\n");
    if (n > 0) 
        printf("receieved request:\n%s", throw_away);
    
}

static void send_response(int client_fd) {
    int total, sent, n;
    total = strlen(RESPONSE);
    sent = 0;
    do {
        n = write(client_fd, RESPONSE + sent, total - sent);
        if (n < 0)
            perror("error sending response\n");
            return;
        if (n == 0)
            return;
        sent += n;
    } while (sent < total);
}
