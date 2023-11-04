from locust import HttpUser, task

class User(HttpUser):
    
    @task
    def user_request(self):
        self.client.get('/')
