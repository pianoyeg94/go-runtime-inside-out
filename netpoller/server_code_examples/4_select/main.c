#include <sys/time.h>
#include <sys/select.h>

// should be performed in a loop
int main() {
    // let's assume that we have 2 sockets in non-blocking 
    // mode already initialized
    int sock1, sock2, socket3, ...;

    // read and write sets of fds to be polled
    fd_set readfds, writefds;
    struct timeval tv;
    
    // clear our read and write fd sets
    FD_ZERO(&readfds);
    FD_ZERO(&writefds);
    
    // advertize that we'll be monitoring sock1 for read events 
    // and sock2 for write events 
    // (add socket fds to their appropriate sets)
    FD_SET(sock1, &readfds);
    FD_SET(sock2, &writefds);
    
    // calculate the largest fd (select requires it)
    int largest_fd = sock1 > sock2 ? sock1 : sock2;
    
    // we'll be polling for 10 seconds
    tv.tv_sec = 10;
    tv.tv_usec = 0;
    
    // poll until a read/write event occurs or timeout elapses
    int ret = select(largest_fd + 1, &readfds, &writefds, NULL, &tv);
    if (ret == -1)
        // error
    else if (ret == 0)
        // we've reached our timeout and no events occured
    else {
        if (FD_ISSET(sock1, &readfds))
            // read event was registered on sock1
            // non-blocking read is available on sock1
            // we can do some processing
        if (FD_ISSET(sock2, &writefds))
            // write event was registered on sock2
            // non-blocking write is available on sock2
            // we can do some processing
    }

    return 0;
}