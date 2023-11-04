#include <sys/epoll.h>

int main() {
    // imaginary inialized connection object
    struct Connection conn;

    int epollfd = epoll_create(0); 
    if ( epollfd < 0 )
        // error

    // initialize a new epoll_event, so we can monitor 
    // read events on our connection
    struct epoll_event ev = {0};
    ev.data.ptr = &conn;
    ev.events = EPOLLIN;

    // register our connection to be monitored by epoll
    if (epoll_ctl(epollfd, EPOLL_CTL_ADD, conn->getSocketFd(), &ev) != 0)
        // error

    // should be inside a loop
    struct epoll_event pevents[20];
    // poll for 10 seconds
    int ready = epoll_wait(epollfd, pevents, 20, 10000);
    if (ret == -1)
        // error
    else if (ret == 0)
        // we've reached our timeout and no events occured
    else {
        // iterate through the ready events list
        for (int i = 0; i < ret; i++) {
            if (pevents[i].events&EPOLLIN) {
                // we can get our connection previosuly stored in the epoll_event
                Connection *c = (Connection*) pevents[i].data.ptr;
                c->handleReadEvent();
            }
        }
    }
}