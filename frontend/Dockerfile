# Build stage
FROM node:26-alpine AS build

WORKDIR /app

# Copy package files
COPY package*.json ./
COPY .npmrc* ./

# Install dependencies
RUN npm ci

# Copy source code
COPY . .

# ARG for build/dev mode
ARG NODE_ENV=production

# If production, build the app, otherwise use development mode
RUN if [ "$NODE_ENV" = "production" ]; then \
        npm run build; \
    fi

# For development, use the node image to run the dev server
FROM build AS development
RUN adduser -D -u 1001 appuser && chown -R appuser:appuser /app
USER appuser
EXPOSE 3000
CMD ["npm", "run", "dev", "--", "--host", "0.0.0.0"]

# For production (default), use nginx-unprivileged to serve the built app (runs as uid 101, no root)
FROM nginxinc/nginx-unprivileged:alpine AS production
# Temporarily switch to root to upgrade OS packages, then back to unprivileged
USER root
RUN apk upgrade --no-cache
USER nginx
COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 8080
CMD ["nginx", "-g", "daemon off;"]
