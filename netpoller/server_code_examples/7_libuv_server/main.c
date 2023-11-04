// gcc -o server main.c /usr/local/lib/libuv.a -pthread -ldl
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <netdb.h>
#include <unistd.h>
#include <sys/socket.h>
#include <uv.h>

static const char INTERFACE[] = "0.0.0.0";
static const int PORT = 8083;
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

static uv_loop_t *loop;

void alloc_buffer(uv_handle_t *handle, size_t suggested_size, uv_buf_t *buf) {
    buf->base = (char*) malloc(suggested_size);
    buf->len = suggested_size;
}

void respond(uv_write_t *req, int status) {
    if (status) 
        fprintf(stderr, "error sending response %s\n", uv_strerror(status));

    free(req);
}

void serve_client(uv_stream_t *client, ssize_t nread, const uv_buf_t *buf) {
    if (nread < 0) {
        if (nread != UV_EOF) {
            fprintf(stderr, "error serving client %s\n", uv_err_name(nread));
            free(buf->base);
            free(client);
            uv_close((uv_handle_t*) client, NULL);
        }
        return; 
    } 
    
    if (nread > 0) {
        uv_write_t *req = (uv_write_t *) malloc(sizeof(uv_write_t));
        uv_buf_t write_buf = uv_buf_init((char*) RESPONSE, sizeof(RESPONSE) + 1);
        uv_write(req, client, &write_buf, 1, respond);
    }

    if (buf->base) 
        free(buf->base);
    
    uv_close((uv_handle_t*) client, NULL);
}

void on_new_connection(uv_stream_t *server, int status) {
    if (status < 0) {
        fprintf(stderr, "error on client connection %s\n", uv_strerror(status));
        return;
    }
        
    uv_tcp_t *client = (uv_tcp_t*) malloc(sizeof(uv_tcp_t));
    uv_tcp_init(loop, client);

    if (uv_accept(server, (uv_stream_t*) client) == 0) 
        uv_read_start((uv_stream_t*) client, alloc_buffer, serve_client);
    else
        uv_close((uv_handle_t*) client, NULL);
}

int main() {
    struct sockaddr_in bind_addr;
    bzero(&bind_addr, sizeof(bind_addr));

    loop = uv_default_loop();

    uv_tcp_t server;
    uv_tcp_init(loop, &server);

    uv_ip4_addr(INTERFACE, PORT, &bind_addr);
    uv_tcp_bind(&server, (struct sockaddr*) &bind_addr, 0);

    int res = uv_listen((uv_stream_t*) &server, BACKLOG, on_new_connection);
    if (res) {
        perror("error listening on address");
        return 1;
    }

    printf("Server listening on %s:%d\n", INTERFACE, PORT);

    return uv_run(loop, UV_RUN_DEFAULT);
}

