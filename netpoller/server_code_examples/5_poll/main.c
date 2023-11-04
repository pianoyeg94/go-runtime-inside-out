#include <poll.h>

int main() {
    // let's assume that we have 2 sockets in non-blocking 
    // mode already initialized
    int sock1, sock2;

    // we'll be monitoring 2 sockets
    struct pollfd fds[2];
    
    // we want to receive read events for sock1
    fds[0].fd = sock1;
    fds[0].events = POLLIN;
    
    // we want to receive write events for sock2
    fds[1].fd = sock2;
    fds[1].events = POLLOUT;
    
    // should be performed in a loop
    // poll for 10 seconds
    int ret = poll(&fds, 2, 10000);
    if (ret == -1)
        // error
    else if (ret == 0)
        // we've reached our timeout and no events occured
    else {
        // got a read event for sock1, need to zero out revents for reuse
        if (fds[0].revents&POLLIN)
            fds[0].revents = 0;
            // non-blocking read is available on sock1
            // we can do some processing
        if (fds[1].revents&POLLOUT)
            fds[1].revents = 0;
            // non-blocking write is available to sock2
            // we can do some processing
    }

    return 0;
}