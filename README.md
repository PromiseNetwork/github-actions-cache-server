# 🚀 GitHub Actions Cache Server

This is a drop-in replacement for the official GitHub hosted cache server. It is compatible with the official `actions/cache` action, so there is no need to change your workflow files and it even works with packages that internally use `actions/cache`.

## Features

- 🔥 **Compatible with official `actions/cache` action**
- 📦 Supports multiple storage solutions and is easily extendable.
- 🔒 Secure and self-hosted, giving you full control over your cache data.
- 😎 Easy setup

```yaml
services:
  cache-server:
    image: ghcr.io/falcondev-oss/github-actions-cache-server
    ports:
      - '3000:3000'
    environment:
      API_BASE_URL: http://localhost:3000
    volumes:
      - cache-data:/app/.data

volumes:
  cache-data:
```

## Building and Publishing to Private Repository

### Prerequisites

- Docker installed and running
- Access to your private container registry
- Docker CLI authenticated with your private registry

### Local Development Build

To build and run the application locally without Docker:

1. **Install dependencies:**

   ```bash
   pnpm install
   ```

2. **Build the application:**

   ```bash
   pnpm run build
   ```

3. **Run the application:**

   ```bash
   pnpm run preview
   ```

4. **For development with hot reload:**
   ```bash
   pnpm run dev
   ```

### Docker Build and Push Steps

1. **Set your private repository environment variable:**

   ```bash
   export CACHE_PRIVATE_REPO=your-private-registry.com/github-actions-cache-server
   ```

2. **Build the Docker image:**

   ```bash
   docker build -t $CACHE_PRIVATE_REPO:latest .
   ```

3. **Tag for your private registry (if needed):**

   ```bash
   docker tag $CACHE_PRIVATE_REPO:latest $CACHE_PRIVATE_REPO:v8.1.4
   ```

4. **Push to your private registry:**
   ```bash
   docker push $CACHE_PRIVATE_REPO:latest
   docker push $CACHE_PRIVATE_REPO:v8.1.4
   ```

### Using Your Private Image

Update your docker-compose.yml or deployment configuration to use your private image:

```yaml
services:
  cache-server:
    image: ${CACHE_PRIVATE_REPO}:latest
    ports:
      - '3000:3000'
    environment:
      API_BASE_URL: http://localhost:3000
    volumes:
      - cache-data:/app/.data

volumes:
  cache-data:
```

### Build Arguments

The Dockerfile supports a `BUILD_HASH` argument for tracking builds:

```bash
docker build --build-arg BUILD_HASH=$(git rev-parse HEAD) -t $CACHE_PRIVATE_REPO:latest .
```

## Documentation

👉 <https://gha-cache-server.falcondev.io/getting-started> 👈
