import uvicorn

async def app(scope, receive, send):
    await send({
        "type": "http.response.start",
        "status": 200,
        "headers": [[b"content-type", b"text/plain"],],
    })
    await send({
        "type": "http.response.body",
        "body": "<html><body><h1>Hello, World!</h1></body></html>".encode(),
        "more_body": False,
    })

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8084)