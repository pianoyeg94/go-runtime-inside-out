#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <signal.h>
#include <unistd.h>
#include <netdb.h>
#include <sys/wait.h>
#include <sys/socket.h>

static const uint16_t INTERFACE = INADDR_ANY;
static const int PORT = 8081;
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
static void serve_client(int);
static void read_request(int);
static void send_response(int);

static int listen_fd;

void sig_handler(int sig_num) {
    pid_t wpid;
    int status = 0;
    while ((wpid = wait(&status)) > 0);

    close(listen_fd);
    exit(sig_num);
}

int main() {
    signal(SIGCHLD, SIG_IGN);
    signal(SIGTERM, sig_handler);
    signal(SIGINT, sig_handler);

    listen_fd = start_server();
    if (listen_fd < 0) 
        return 1;
    
    printf("Server listening on 0.0.0.0:%d\n", PORT);

    while(1) {
        int client_fd = accept(listen_fd, (struct sockaddr*) NULL, (socklen_t*) NULL);
        if (client_fd < 0) {
            perror("error accepting client connection");
            return 1;
        }

        pid_t pid = fork();
        if (pid < 0) {
            perror("error handling client connection");
            return 1;
        } 

        // in child process
        if (pid == 0) {
            close(listen_fd);
            serve_client(client_fd);
            exit(0);
        }

        // in parent process
        close(client_fd);
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

static void serve_client(int client_fd) {
    read_request(client_fd);
    send_response(client_fd);

    shutdown(client_fd, SHUT_RDWR);
    close(client_fd);
}

static void read_request(int client_fd) {
    char throw_away[4096];
    memset(throw_away, 0, sizeof(throw_away));
    int n = read(client_fd, throw_away, sizeof(throw_away)-1);
    if (n > 0) 
        fprintf(stderr, "receieved request:\n%s", throw_away);
}

static void send_response(int client_fd) {
    int total, sent, n;
    total = strlen(RESPONSE);
    sent = 0;
    do {
        n = write(client_fd, RESPONSE + sent, total - sent);
        if (n < 0)
            fprintf(stderr, "error sending response\n");
            return;
        if (n == 0)
            return;
        sent += n;
    } while (sent < total);
}

