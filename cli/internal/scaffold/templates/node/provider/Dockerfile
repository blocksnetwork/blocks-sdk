FROM node:24.22.1-alpine
WORKDIR /app
COPY package*.json ./
RUN npm ci --omit=dev
COPY . .
CMD ["npx", "blocks", "run"]
